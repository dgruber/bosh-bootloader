package fakes

import (
	"github.com/cloudfoundry/bosh-bootloader/bosh"
	"golang.org/x/net/proxy"
)

type BOSHClient struct {
	UpdateCloudConfigCall struct {
		CallCount int
		Receives  struct {
			Yaml []byte
		}
		Returns struct {
			Error error
		}
	}

	ConfigureHTTPClientCall struct {
		CallCount int
		Receives  struct {
			Socks5Client proxy.Dialer
		}
	}

	InfoCall struct {
		CallCount int
		Returns   struct {
			Info  bosh.Info
			Error error
		}
	}
}

func (c *BOSHClient) UpdateCloudConfig(yaml []byte) error {
	c.UpdateCloudConfigCall.CallCount++
	c.UpdateCloudConfigCall.Receives.Yaml = yaml
	return c.UpdateCloudConfigCall.Returns.Error
}

func (c *BOSHClient) ConfigureHTTPClient(socks5Client proxy.Dialer) {
	c.ConfigureHTTPClientCall.CallCount++
	c.ConfigureHTTPClientCall.Receives.Socks5Client = socks5Client
}

func (c *BOSHClient) Info() (bosh.Info, error) {
	c.InfoCall.CallCount++
	return c.InfoCall.Returns.Info, c.InfoCall.Returns.Error
}
