package buildcache

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/docker/distribution/digest"
	engineapi "github.com/docker/engine-api/client"
	"golang.org/x/net/context"
)

type buildCache struct {
	client *engineapi.Client
}

func New(client *engineapi.Client) *buildCache {
	return &buildCache{
		client: client,
	}
}

func (b *buildCache) Get(ctx context.Context, graphdir, image string) (io.ReadCloser, error) {
	id, err := b.getImageID(ctx, image)
	if err != nil {
		return nil, err
	}
	info, err := b.client.Info(ctx)
	if err != nil {
		return nil, err
	}
	if graphdir == "" {
		graphdir = info.DockerRootDir
	}
	imagedir := filepath.Join(graphdir, "image", info.Driver)

	if _, err := os.Stat(filepath.Join(imagedir, "imagedb/content/sha256", id.Hex())); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Could not access files from the Docker storage directory %v. This application currently requires direct access to this directory for saving build cache. Use \"--graph\" option to specify different folder.", graphdir)
		}
	}
	pc, err := b.getParentChain(ctx, imagedir, id)
	if err != nil {
		return nil, err
	}
	if err := validateParentChain(pc); err != nil {
		return nil, err
	}

	return b.writeCacheTar(ctx, pc), nil
}

func (b *buildCache) writeCacheTar(ctx context.Context, imgs []image) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		archive := tar.NewWriter(gz)
		var mfst []manifestRow
		for _, img := range imgs {
			if ctx.Err() != nil {
				pw.CloseWithError(ctx.Err())
			}
			if err := archive.WriteHeader(&tar.Header{
				Name: img.id.Hex() + ".json",
				Size: int64(len(img.raw)),
				Mode: 0444,
			}); err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := archive.Write(img.raw); err != nil {
				pw.CloseWithError(err)
				return
			}
			mfst = append(mfst, manifestRow{
				Config: img.id.Hex() + ".json",
				Parent: img.parent.String(),
				Layers: img.layers,
			})
		}
		mfstData, err := json.Marshal(mfst)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := archive.WriteHeader(&tar.Header{
			Name: "manifest.json",
			Size: int64(len(mfstData)),
			Mode: 0444,
		}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := archive.Write(mfstData); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := archive.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := gz.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	go func() {
		<-ctx.Done()
		pw.CloseWithError(ctx.Err())
	}()
	return pr
}

func (b *buildCache) getImageID(ctx context.Context, ref string) (digest.Digest, error) {
	inspect, _, err := b.client.ImageInspectWithRaw(ctx, ref, false)
	if err != nil {
		return "", err
	}
	return digest.ParseDigest(inspect.ID)
}

func (b *buildCache) getParentChain(ctx context.Context, dir string, id digest.Digest) ([]image, error) {
	config, err := ioutil.ReadFile(filepath.Join(dir, "imagedb/content/sha256", id.Hex()))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	img, err := parseImage(config)
	if err != nil {
		return nil, err
	}
	if img.id != id {
		return nil, fmt.Errorf("invalid configuration for %v, got id %v", id, img.id)
	}
	parent, err := ioutil.ReadFile(filepath.Join(dir, "imagedb/metadata/sha256", id.Hex(), "parent"))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		if os.IsNotExist(err) {
			return []image{*img}, nil
		}
		return nil, err
	}

	parentID, err := digest.ParseDigest(string(parent))
	if err != nil {
		return nil, err
	}
	img.parent = parentID

	pc, err := b.getParentChain(ctx, dir, parentID)
	if err != nil {
		return nil, err
	}
	return append([]image{*img}, pc...), nil
}

type image struct {
	raw    []byte
	id     digest.Digest
	parent digest.Digest
	layers []digest.Digest
}

type manifestRow struct {
	Config string
	Parent string `json:",omitempty"`
	Layers []digest.Digest
}

func parseImage(in []byte) (*image, error) {
	var conf struct {
		RootFS struct {
			DiffIDs []digest.Digest `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(in, &conf); err != nil {
		return nil, err
	}
	return &image{
		layers: conf.RootFS.DiffIDs,
		raw:    in,
		id:     digest.FromBytes(in),
	}, nil
}

func validateParentChain(imgs []image) error {
	if len(imgs) < 2 {
		return nil
	}
	if err := validateParentChain(imgs[1:]); err != nil {
		return err
	}
	for i, l := range imgs[1].layers {
		if l != imgs[0].layers[i] {
			return fmt.Errorf("invalid layers in parent chain")
		}
	}
	return nil
}
