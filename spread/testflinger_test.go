package spread_test

import (
	"github.com/canonical/spread-plus/spread"

	. "gopkg.in/check.v1"
)

type TestFlingerSuite struct{}

var _ = Suite(&TestFlingerSuite{})

func (s *TestFlingerSuite) TestBuildProvisionDataImageURL(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "https://example.com/ubuntu.img.xz",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, map[string]interface{}{
		"url": "https://example.com/ubuntu.img.xz",
	})
}

func (s *TestFlingerSuite) TestBuildProvisionDataImageDistro(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "core22-latest-stable",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, map[string]interface{}{
		"distro": "core22-latest-stable",
	})
}

func (s *TestFlingerSuite) TestBuildProvisionDataNoImage(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "ubuntu-classic",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, IsNil)
}

func (s *TestFlingerSuite) TestBuildProvisionDataEmptyImage(c *C) {
	system := &spread.System{
		Name: "ubuntu-classic",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, IsNil)
}

func (s *TestFlingerSuite) TestBuildProvisionDataExplicit(c *C) {
	provisionData := map[string]interface{}{
		"distro":    "jammy",
		"kernel":    "hwe-22.04",
		"user_data": "#cloud-config\n",
	}
	system := &spread.System{
		Name:          "ubuntu-classic",
		ProvisionData: provisionData,
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, provisionData)
}

func (s *TestFlingerSuite) TestBuildProvisionDataImageAndProvisionDataFails(c *C) {
	provisionData := map[string]interface{}{
		"url":          "https://example.com/override.img.xz",
		"token_file":   "/run/token",
		"redeploy_cfg": "reset",
	}
	system := &spread.System{
		Name:          "ubuntu-classic",
		Image:         "https://example.com/ignored.img.xz",
		ProvisionData: provisionData,
	}
	// Setting both image and provision-data is ambiguous and must fail.
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(pdata, IsNil)
	c.Assert(err, ErrorMatches, "system ubuntu-classic sets both image and provision-data; set only one")
}
