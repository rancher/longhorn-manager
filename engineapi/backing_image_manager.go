package engineapi

import (
	"fmt"

	bimapi "github.com/longhorn/backing-image-manager/api"
	bimclient "github.com/longhorn/backing-image-manager/pkg/client"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	"github.com/longhorn/longhorn-manager/types"
)

const (
	BackingImageManagerDefaultPort = 8000

	CurrentBackingImageManagerAPIVersion = 1
	MinBackingImageManagerAPIVersion     = 1
	UnknownBackingImageManagerAPIVersion = 0
)

type BackingImageManagerClient struct {
	ip            string
	apiMinVersion int
	apiVersion    int

	grpcClient *bimclient.BackingImageManagerClient
}

func CheckBackingImageManagerCompatibilty(bimMinVersion, bimVersion int) error {
	if MinBackingImageManagerAPIVersion > bimVersion || CurrentBackingImageManagerAPIVersion < bimMinVersion {
		return fmt.Errorf("current-min API version used by longhorn manager %v-%v is not compatible with BackingImageManager current-min APIVersion %v-%v",
			CurrentBackingImageManagerAPIVersion, MinBackingImageManagerAPIVersion, bimVersion, bimMinVersion)
	}
	return nil
}

func NewBackingImageManagerClient(bim *longhorn.BackingImageManager) (*BackingImageManagerClient, error) {
	if bim.Status.CurrentState != types.BackingImageManagerStateRunning || bim.Status.IP == "" {
		return nil, fmt.Errorf("invalid Backing Image Manager %v, state: %v, IP: %v", bim.Name, bim.Status.CurrentState, bim.Status.IP)
	}
	if bim.Status.APIMinVersion != UnknownBackingImageManagerAPIVersion {
		if err := CheckBackingImageManagerCompatibilty(bim.Status.APIMinVersion, bim.Status.APIVersion); err != nil {
			return nil, fmt.Errorf("cannot launch a client for incompatible backing image manager %v", bim.Name)
		}
	}

	return &BackingImageManagerClient{
		ip:            bim.Status.IP,
		apiMinVersion: bim.Status.APIMinVersion,
		apiVersion:    bim.Status.APIVersion,
		grpcClient:    bimclient.NewBackingImageManagerClient(fmt.Sprintf("%s:%d", bim.Status.IP, BackingImageManagerDefaultPort)),
	}, nil
}

func (c *BackingImageManagerClient) parseBackingImageFileInfo(bi *bimapi.BackingImage) *types.BackingImageFileInfo {
	if bi == nil {
		return nil
	}
	return &types.BackingImageFileInfo{
		Name:      bi.Name,
		URL:       bi.URL,
		UUID:      bi.UUID,
		Size:      bi.Size,
		Directory: bi.Directory,

		State:                types.BackingImageDownloadState(bi.Status.State),
		Message:              bi.Status.ErrorMsg,
		SendingReference:     bi.Status.SendingReference,
		SenderManagerAddress: bi.Status.SenderManagerAddress,
		DownloadProgress:     bi.Status.DownloadProgress,
	}
}

func (c *BackingImageManagerClient) Pull(name, url, uuid string) (*types.BackingImageFileInfo, error) {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return nil, err
	}
	resp, err := c.grpcClient.Pull(name, url, uuid)
	if err != nil {
		return nil, err
	}
	return c.parseBackingImageFileInfo(resp), nil
}

func (c *BackingImageManagerClient) Sync(name, url, uuid, fromHost, toHost string, size int64) (*types.BackingImageFileInfo, error) {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return nil, err
	}
	resp, err := c.grpcClient.Sync(name, url, uuid, fromHost, toHost, size)
	if err != nil {
		return nil, err
	}
	return c.parseBackingImageFileInfo(resp), nil
}

func (c *BackingImageManagerClient) Delete(name string) error {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return err
	}
	return c.grpcClient.Delete(name)
}

func (c *BackingImageManagerClient) Get(name string) (*types.BackingImageFileInfo, error) {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return nil, err
	}
	backingImage, err := c.grpcClient.Get(name)
	if err != nil {
		return nil, err
	}
	return c.parseBackingImageFileInfo(backingImage), nil
}

func (c *BackingImageManagerClient) List() (map[string]types.BackingImageFileInfo, error) {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return nil, err
	}
	backingImages, err := c.grpcClient.List()
	if err != nil {
		return nil, err
	}
	result := map[string]types.BackingImageFileInfo{}
	for name, backingImage := range backingImages {
		result[name] = *c.parseBackingImageFileInfo(backingImage)
	}
	return result, nil
}

func (c *BackingImageManagerClient) Watch() (*bimapi.BackingImageStream, error) {
	if err := CheckBackingImageManagerCompatibilty(c.apiMinVersion, c.apiVersion); err != nil {
		return nil, err
	}
	return c.grpcClient.Watch()
}

func (c *BackingImageManagerClient) VersionGet() (int, int, error) {
	output, err := c.grpcClient.VersionGet()
	if err != nil {
		return 0, 0, err
	}
	return output.BackingImageManagerAPIMinVersion, output.BackingImageManagerAPIVersion, nil
}
