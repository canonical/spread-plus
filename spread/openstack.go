package spread

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gooseclient "github.com/go-goose/goose/v5/client"
	goosehttp "github.com/go-goose/goose/v5/http"
	"github.com/joho/godotenv"

	"github.com/go-goose/goose/v5/glance"
	"github.com/go-goose/goose/v5/identity"
	"github.com/go-goose/goose/v5/neutron"
	"github.com/go-goose/goose/v5/nova"

	"golang.org/x/net/context"
)

func Openstack(p *Project, b *Backend, o *Options) Provider {
	return &openstackProvider{
		project: p,
		backend: b,
		options: o,
	}
}

const (
	ACTIVE = "active"
)

type glanceImageClient interface {
	ListImagesDetail() ([]glance.ImageDetail, error)
}

type novaComputeClient interface {
	ListFlavors() ([]nova.Entity, error)
	ListAvailabilityZones() ([]nova.AvailabilityZone, error)
	GetServer(serverId string) (*nova.ServerDetail, error)
	ListServersDetail(filter *nova.Filter) ([]nova.ServerDetail, error)
	ListVolumeAttachments(serverId string) ([]nova.VolumeAttachment, error)
	RunServer(opts nova.RunServerOpts) (*nova.Entity, error)
	DeleteServer(serverId string) error
}

type openstackServices struct {
	compute OpenstackService
	volume  OpenstackService
}

type OpenstackService struct {
	Name     string
	version  string
	endpoint *url.URL
}

type openstackProvider struct {
	project *Project
	backend *Backend
	options *Options

	region        string
	osClient      gooseclient.AuthenticatingClient
	computeClient novaComputeClient
	networkClient *neutron.Client
	imageClient   glanceImageClient

	mu sync.Mutex

	keyChecked bool
	keyErr     error
}

type openstackServer struct {
	p *openstackProvider
	d openstackServerData

	system  *System
	address string
}

type openstackServerData struct {
	Id                  string
	Name                string
	Flavor              string    `json:"machineType"`
	Networks            []string  `json:"network"`
	Status              string    `yaml:"-"`
	Created             time.Time `json:"creationTimestamp"`
	DeleteOnTermination bool

	Labels map[string]string `yaml:"-"`
}

type openstackVolumeData struct {
	Id          string             `json:"id"`
	Status      string             `json:"status"`
	SnapshotId  string             `json:"snapshot_id,omitempty"`
	Attachments []VolumeAttachment `json:"attachments,omitempty"`
	Name        string             `json:"name,omitempty"`
}

type openstackSnapshotData struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Status   string `json:"status"`
	VolumeID string `json:"volume_id,omitempty"`
}

type VolumeAttachment struct {
	Id       string `json:"id"`
	ServerId string `json:"server_id"`
	VolumeId string `json:"volume_id"`
}

type VolumeActionResetStatus struct {
	Action VolumeActionResetStatusDetails `json:"os-reset_status"`
}

type VolumeActionResetStatusDetails struct {
	Status string `json:"status"`
}

func (s *openstackServer) String() string {
	if s.system == nil {
		return s.d.Name
	}
	return fmt.Sprintf("%s (%s)", s.system, s.d.Name)
}

func (s *openstackServer) Label() string {
	return s.d.Name
}

func (s *openstackServer) Provider() Provider {
	return s.p
}

func (s *openstackServer) Address() string {
	return s.address
}

func (s *openstackServer) System() *System {
	return s.system
}

func (s *openstackServer) ReuseData() interface{} {
	return &s.d
}

var openstackSerialOutputTimeout = 30 * time.Second
var openstackSerialConsoleErr = fmt.Errorf("cannot get console output")

func (s *openstackServer) SerialOutput(ctx context.Context) (string, error) {
	_, err := s.p.computeClient.GetServer(s.d.Id)
	if err != nil {
		// this is when the server is removed
		return "Server removed", nil
	}

	url := fmt.Sprintf("servers/%s/action", s.d.Id)

	var req struct {
		OsGetSerialConsole struct{} `json:"os-getConsoleOutput"`
	}
	var resp struct {
		Output string `json:"output"`
	}
	requestData := goosehttp.RequestData{ReqValue: req, RespValue: &resp, ExpectedStatus: []int{http.StatusOK}}
	timeout := time.After(openstackSerialOutputTimeout)
	retry := time.NewTicker(openstackServerBootRetry)
	defer retry.Stop()

	for {
		err := s.p.osClient.SendRequest("POST", "compute", "v2", url, &requestData)
		if err != nil {
			debugf("failed to retrieve the serial console for server %s: %v", s, err)
		}
		if len(resp.Output) > 0 {
			return resp.Output, nil
		}
		select {
		case <-retry.C:
		case <-timeout:
			return "", fmt.Errorf("failed to retrieve the serial console for instance %s: timeout reached", s)
		case <-ctx.Done():
			return "", fmt.Errorf("failed to retrieve the serial console for instance %s: interrupted", s)
		}
	}
}

const (
	serverStatusProvisioning = "PROVISIONING"
	volumeStatusAvailable    = "available"
	volumeStatusError        = "error"
)

func (p *openstackProvider) Backend() *Backend {
	return p.backend
}

func (p *openstackProvider) Reuse(ctx context.Context, rsystem *ReuseSystem, system *System) (Server, error) {
	s := &openstackServer{
		p:       p,
		address: rsystem.Address,
		system:  system,
	}
	err := rsystem.UnmarshalData(&s.d)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal openstack reuse data: %v", err)
	}
	return s, nil
}

func (p *openstackProvider) Allocate(ctx context.Context, system *System) (Server, error) {
	if err := p.checkKey(); err != nil {
		return nil, err
	}

	s, err := p.createMachine(ctx, system)
	if err != nil {
		return nil, err
	}

	printf("Allocated %s.", s)

	err = p.updateAddressIfProxyDefined(ctx, s)
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *openstackServer) Discard(ctx context.Context) error {
	return s.p.removeMachine(ctx, s)
}

const openstackCloudInitScript = `
#cloud-config
runcmd:
  - echo root:%s | chpasswd
  - sed -i 's/^\s*#\?\s*\(PermitRootLogin\|PasswordAuthentication\)\>.*/\1 yes/' /etc/ssh/sshd_config
  - sed -i 's/^PermitRootLogin=/#PermitRootLogin=/g' /etc/ssh/sshd_config.d/* || true
  - sed -i 's/^PasswordAuthentication=/#PasswordAuthentication=/g' /etc/ssh/sshd_config.d/* || true
  - test -d /etc/ssh/sshd_config.d && echo 'PermitRootLogin=yes' > /etc/ssh/sshd_config.d/00-spread.conf
  - test -d /etc/ssh/sshd_config.d && echo 'PasswordAuthentication=yes' >> /etc/ssh/sshd_config.d/00-spread.conf
  - pkill -o -HUP sshd || true
  - test -c /dev/ttyS0 && echo '` + openstackReadyMarker + `' 1>/dev/ttyS0 2>/dev/null || true
  - test -c /dev/ttyAMA0 && echo '` + openstackReadyMarker + `' 1>/dev/ttyAMA0 2>/dev/null || true
  - test -c /dev/console && echo '` + openstackReadyMarker + `' 1>/dev/console 2>/dev/null || true
`

const openstackReadyMarker = "MACHINE-IS-READY"
const openstackNameLayout = "Jan021504.000000"
const openstackDefaultFlavor = "m1.medium"

var timeNow = time.Now

func openstackName() string {
	return strings.ToLower(strings.Replace(timeNow().UTC().Format(openstackNameLayout), ".", "-", 1))
}

type openstackError struct {
	gooseError error
}

// Error messages triggered by modules are string like:
// line 1...n is a high level description about the error like:
// caused by: <highlevel_cause>
// line n+1 contains the error cause and the error info following the formats:
// caused by: <error_cause> ; error info: <json_data>
// caused by: <error_cause> ; error info: <html_data>
//
// This code will find the *last* "caused by:" line as it generally tells
// the most details.
// TODO: check if the above idea works well in practice, otherwise switch
// to e.g. firstLine
func (e *openstackError) cause(errMsg string) string {
	debugf("Full openstack error message: %v", errMsg)

	causedBy := strings.Split(errMsg, "caused by:")
	if len(causedBy) <= 1 {
		return errMsg
	}
	lastCausedBy := causedBy[len(causedBy)-1]
	firstErrLine := strings.SplitN(lastCausedBy, "\n", 2)[0]
	return strings.TrimSpace(firstErrLine)
}

func (e *openstackError) Error() string {
	msg := e.gooseError.Error()

	causedByErrorMsg := e.cause(msg)
	if causedByErrorMsg != "" {
		return causedByErrorMsg
	}

	return msg
}

func (p *openstackProvider) findFlavor(flavorName string) (*nova.Entity, error) {
	flavors, err := p.computeClient.ListFlavors()
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve flavors list: %v", &openstackError{err})
	}

	for _, f := range flavors {
		if f.Name == flavorName {
			return &f, nil
		}
	}

	return nil, &FatalError{fmt.Errorf("cannot find valid flavor with name %q", flavorName)}
}

func (p *openstackProvider) findFirstNetwork() (*neutron.NetworkV2, error) {
	networks, err := p.networkClient.ListNetworksV2()
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve networks list: %v", &openstackError{err})
	}
	// When there are not networks defined, the first network which is not external
	// is returned (external networks could not be allowed to request)
	for _, net := range networks {
		if !net.External {
			return &net, nil
		}
	}
	return nil, &FatalError{errors.New("cannot find valid network to create floating IP")}
}

func (p *openstackProvider) findNetwork(name string) (*neutron.NetworkV2, error) {
	networks, err := p.networkClient.ListNetworksV2()
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve networks list: %v", &openstackError{err})
	}

	for _, net := range networks {
		if name != "" && name == net.Name {
			return &net, nil
		}
	}
	return nil, &FatalError{fmt.Errorf("cannot find valid network with name %q", name)}
}

func (p *openstackProvider) getActiveImages() ([]glance.ImageDetail, error) {
	images, err := p.imageClient.ListImagesDetail()
	activeImages := []glance.ImageDetail{}

	if err != nil {
		return nil, fmt.Errorf("cannot retrieve images list: %v", &openstackError{err})
	}

	for _, image := range images {
		if strings.ToLower(image.Status) == strings.ToLower(ACTIVE) {
			activeImages = append(activeImages, image)
		}
	}
	return activeImages, nil
}

func (p *openstackProvider) findImage(imageName string) (*glance.ImageDetail, error) {
	var lastImage glance.ImageDetail
	var lastCreatedDate time.Time

	// TODO: consider using an image cache just like the google backend
	// (https://github.com/snapcore/spread/pull/175 needs to be fixed first)
	images, err := p.getActiveImages()

	if err != nil {
		return nil, fmt.Errorf("cannot get the active images list: %v", &openstackError{err})
	}

	// In openstack there are no "project" specific images and no
	// concept of a "family" so the lookup is similar but it checks
	// by prefix instead of the family. The matching is done in the following order:
	// a) exact match, b) prefix and c) term match. If multiple
	// term matches  prefixes are found the newest image is selected.

	// First consider exact matches on name.
	for _, image := range images {
		if image.Name == imageName {
			// return the image when it matchs exactly with the provided name
			debugf("Name match: %#v matches %#v", imageName, image)
			return &image, nil
		}
	}

	// Second, use prefix matching. The image is selected when the name of the image starts
	// with the provided name. When multiple images match, the newest one is selected.
	for _, image := range images {
		if strings.HasPrefix(image.Name, imageName) {
			debugf("Prefix match: %#v matches %#v", imageName, image)
			// Check if the creation date for the current image is after the previous selected one
			currCreatedDate, err := time.Parse(time.RFC3339, image.Created)
			// When the creation date is not set or it cannot be parsed, it is considered as created just now
			if err != nil {
				currCreatedDate = time.Time{}
			}

			// Save the image when either it is the first match or it is newer than the previous match
			if lastImage.Id == "" || currCreatedDate.After(lastCreatedDate) {
				debugf("Newer prefix match found: %#v", image)
				lastImage = image
				lastCreatedDate = currCreatedDate
			}
		}
	}

	if lastImage.Id != "" {
		return &lastImage, nil
	}

	// Third, use term matching. The image is selected when all the terms in the provided
	// name are contained in the image name. When multiple images match, the newest one is selected.
	searchTerms := toTerms(imageName)
	for _, image := range images {
		imageTerms := toTerms(image.Name)
		if containsTerms(imageTerms, searchTerms) {
			debugf("Terms match: %#v matches %#v", imageName, image)
			// Check if the creation date for the current image is after the previous selected one
			currCreatedDate, err := time.Parse(time.RFC3339, image.Created)
			// When the creation date is not set or it cannot be parsed, it is considered as created just now
			if err != nil {
				currCreatedDate = time.Time{}
			}

			// Save the image when either it is the first match or it is newer than the previous match
			if lastImage.Id == "" || currCreatedDate.After(lastCreatedDate) {
				debugf("Newer terms match found: %#v", image)
				lastImage = image
				lastCreatedDate = currCreatedDate
			}
		}
	}

	if lastImage.Id != "" {
		return &lastImage, nil
	}

	return nil, &FatalError{fmt.Errorf("cannot find matching image for %q", imageName)}
}

func (p *openstackProvider) findSecurityGroupNames(names []string) ([]nova.SecurityGroupName, error) {
	var secGroupNames []nova.SecurityGroupName

	secGroups, err := p.networkClient.ListSecurityGroupsV2()
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve security groups: %v", &openstackError{err})
	}

	if len(secGroups) == 0 {
		return nil, &FatalError{errors.New("cannot find any groups")}
	}

	for _, name := range names {
		found := false
		for _, sg := range secGroups {
			if name == sg.Name {
				found = true
				break
			}
		}
		if !found {
			return nil, &FatalError{fmt.Errorf("cannot find valid group with name %q", name)}
		}
		secGroupNames = append(secGroupNames, nova.SecurityGroupName{Name: name})
	}
	return secGroupNames, nil
}

var openstackProvisionTimeout = 3 * time.Minute
var openstackProvisionRetry = 5 * time.Second

func (p *openstackProvider) saveProvisioningOutput(s *openstackServer, detail *nova.ServerDetail) error {
	// output is saved just if the status is error and fault object set
	if detail.Status != nova.StatusError || detail.Fault == nil {
		return nil
	}

	// Use the name to identify the file
	resultsFile := s.d.Name + ".result.log"

	// Build the output to write to the log
	var buffer bytes.Buffer
	buffer.WriteString(detail.Fault.Message)
	buffer.WriteString(detail.Fault.Details)

	err := saveLog(p.options.Logs, resultsFile, buffer.Bytes())
	if err != nil {
		return err
	}

	printf("Allocation output for instance %s saved to %s/%s", s.d.Name, p.options.Logs, resultsFile)
	return nil
}

func (p *openstackProvider) waitProvision(ctx context.Context, s *openstackServer) error {
	debugf("Waiting for %s to provision...", s)

	wait_timeout := s.system.WaitTimeout.Duration
	timeout := time.After(openstackProvisionTimeout)
	if wait_timeout != 0 {
		timeout = time.After(wait_timeout)
	}
	retry := time.NewTicker(openstackProvisionRetry)
	defer retry.Stop()

	for {
		select {
		case <-timeout:
			server, err := p.computeClient.GetServer(s.d.Id)
			if err != nil {
				return fmt.Errorf("timeout waiting for %s to provision", s.d.Id)
			}
			if server.Fault == nil {
				return fmt.Errorf("timeout waiting for %s to provision, status: %s", s.d.Id, server.Status)
			}
			return fmt.Errorf("timeout waiting for %s to provision, error: %s", s, server.Fault.Message)

		case <-retry.C:
			server, err := p.computeClient.GetServer(s.d.Id)
			if err != nil {
				debugf("cannot get instance info: %v", &openstackError{err})
				continue
			}
			if server.Status != nova.StatusBuild {
				if server.Status != nova.StatusActive {
					if p.options.Logs != "" {
						// Use the uuid to identify the file
						err = p.saveProvisioningOutput(s, server)
						if err != nil {
							return fmt.Errorf("error saving provisioning result output: %v", err)
						}
					}
					if server.Status != nova.StatusError || server.Fault == nil {
						return fmt.Errorf("cannot use server with status %s", server.Status)
					}
					return fmt.Errorf(server.Fault.Message)
				}
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("cannot wait for %s to provision: interrupted", s)
		}
	}
}

var openstackServerBootTimeout = 5 * time.Minute
var openstackServerBootRetry = 5 * time.Second

func countIPsBetween(initialIp net.IP, finalIp net.IP) (uint32, error) {
	ip1Int := binary.BigEndian.Uint32(initialIp.To4())
	ip2Int := binary.BigEndian.Uint32(finalIp.To4())

	if ip1Int > ip2Int {
		return ip1Int - ip2Int, nil
	}
	return ip2Int - ip1Int, nil
}

func (p *openstackProvider) updateAddressIfProxyDefined(ctx context.Context, s *openstackServer) error {
	if s.p.backend.Proxy != "" {
		if len(s.p.backend.CIDRPortRel) == 0 {
			return fmt.Errorf("cidr and port relation is required when proxy is defined")
		}
	} else {
		return nil
	}

	for _, rel := range s.p.backend.CIDRPortRel {
		parts := strings.SplitN(rel, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("error with cidr to port relation, cidr:port expected but %s obtained", rel)
		}
		cidr := parts[0]
		initialPort, err := strconv.Atoi(parts[1])
		if err != nil {
			return fmt.Errorf("error parsing initial port, invalid port format: %s", err)
		}

		ip := net.ParseIP(s.address)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid IPv4 addresses")
		}

		_, cidrNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("Invalid CIDR format: %s", err)
		}

		if cidrNet.Contains(ip) {
			initialIp := net.ParseIP(strings.SplitN(cidr, "/", 2)[0])
			if initialIp == nil || initialIp.To4() == nil {
				return fmt.Errorf("invalid Initial IPv4 addresses from CIDR")
			}

			diffBetweenIps, err := countIPsBetween(initialIp, ip)
			if err != nil {
				return fmt.Errorf("Invalid CIDR format: %s", err)
			}
			finalPort := strconv.Itoa(initialPort + int(diffBetweenIps))
			s.address = s.p.backend.Proxy + ":" + finalPort
			printf("Server connected through proxy %s", s.address)
		}
	}
	return nil
}

func (p *openstackProvider) waitServerBootSerial(ctx context.Context, s *openstackServer) error {
	timeout := time.After(openstackServerBootTimeout)
	relog := time.NewTicker(60 * time.Second)
	defer relog.Stop()
	retry := time.NewTicker(openstackServerBootRetry)
	defer retry.Stop()

	var marker = openstackReadyMarker
	for {
		resp, err := s.SerialOutput(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", openstackSerialConsoleErr, err)
		}

		if strings.Contains(resp, marker) {
			return nil
		}

		select {
		case <-retry.C:
			debugf("Server %s is taking a while to boot...", s)
		case <-relog.C:
			printf("Server %s is taking a while to boot...", s)
		case <-timeout:
			return fmt.Errorf("cannot find ready marker in console output for %s: timeout reached", s)
		case <-ctx.Done():
			return fmt.Errorf("cannot wait for %s to boot: interrupted", s)
		}
	}
}

func (p *openstackProvider) waitServerBoot(ctx context.Context, s *openstackServer) error {
	printf("Waiting for %s to boot at %s...", s, s.address)
	err := p.waitServerBootSerial(ctx, s)
	if err != nil {
		if !errors.Is(err, openstackSerialConsoleErr) {
			return err
		}
	}
	return nil
}

func (p *openstackProvider) address(ctx context.Context, s *openstackServer) (string, error) {
	timeout := time.After(30 * time.Second)
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()

	for {
		server, err := p.computeClient.GetServer(s.d.Id)
		if err != nil {
			return "", fmt.Errorf("cannot get IP address for Openstack instance %s: %v", s, &openstackError{err})
		}
		// The addresses for a network is map of networks to a list of ip adresses
		// We are configuring just 1 network address for the network
		// To get the address is used the first network

		if len(server.Addresses) > 0 {
			if len(s.d.Networks) > 0 {
				return server.Addresses[s.d.Networks[0]][0].Address, nil
			}
			// When no network selected, the we use the first ip in addresses
			for _, net_val := range server.Addresses {
				if len(net_val[0].Address) > 0 {
					return net_val[0].Address, nil
				}
			}
		}

		select {
		case <-retry.C:
			debugf("Server %s is taking a while to get IP address...", s)
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for Openstack instance %s IP address", s)
		case <-ctx.Done():
			return "", fmt.Errorf("cannot wait for %s IP address: interrupted", s)
		}
	}
}

func (p *openstackProvider) createMachine(ctx context.Context, system *System) (*openstackServer, error) {
	debugf("Creating new openstack server for %s...", system.Name)

	name := openstackName()
	flavorName := openstackDefaultFlavor
	if system.Plan != "" {
		flavorName = system.Plan
	}
	flavor, err := p.findFlavor(flavorName)
	if err != nil {
		return nil, err
	}

	networks := []nova.ServerNetworks{}
	if len(system.Networks) == 0 {
		net, err := p.findFirstNetwork()
		if err != nil {
			return nil, err
		}
		networks = []nova.ServerNetworks{{NetworkId: net.Id}}
	}

	for _, netName := range system.Networks {
		net, err := p.findNetwork(netName)
		if err != nil {
			return nil, err
		}
		networks = append(networks, nova.ServerNetworks{NetworkId: net.Id})
	}

	image, err := p.findImage(system.Image)
	if err != nil {
		return nil, err
	}

	// cloud init script
	cloudconfig := fmt.Sprintf(openstackCloudInitScript, p.options.Password)

	// tags to the created instance
	tags := map[string]string{
		"spread":   "true",
		"owner":    strings.ToLower(username()),
		"reuse":    strconv.FormatBool(p.options.Reuse),
		"password": p.options.Password,
	}

	// halt-timeout is added to the server to determine the time when it
	// has to be garbage collected
	if p.backend.HaltTimeout.Duration != 0 {
		tags["halt-timeout"] = p.backend.HaltTimeout.Duration.String()
	}

	opts := nova.RunServerOpts{
		Name:        name,
		ImageId:     image.Id,
		FlavorId:    flavor.Id,
		Networks:    networks,
		Metadata:    tags,
		UserData:    []byte(cloudconfig),
		ConfigDrive: true, // needed because of CVE-2024-6174
	}

	// When the storage size is defined, then we use the volume generated
	// with the source image. Otherwise we boot in an ephemeral disk the
	// image using the size described in the flavor
	storage := image.MinimumDisk
	if system.Storage != 0 {
		storage = int(system.Storage / gb)
	}
	// We use 20 GB as default value for the storage size
	if storage == 0 {
		storage = 20
	}

	// By default volumes are deleted on termination
	deleteOnTermination := false
	if p.backend.VolumeAutoDelete == nil {
		deleteOnTermination = true
	} else {
		deleteOnTermination = *p.backend.VolumeAutoDelete
	}

	opts.BlockDeviceMappings = []nova.BlockDeviceMapping{{
		BootIndex:           0,
		SourceType:          "image",
		DestinationType:     "volume",
		VolumeSize:          storage,
		UUID:                image.Id,
		DeleteOnTermination: deleteOnTermination,
	}}

	if len(system.Groups) > 0 {
		sgNames, err := p.findSecurityGroupNames(system.Groups)
		if err != nil {
			return nil, err
		}
		opts.SecurityGroupNames = sgNames
	}

	if len(p.backend.Location) > 0 {
		opts.AvailabilityZone = p.backend.Location
	}

	server, err := p.computeClient.RunServer(opts)
	if err != nil {
		return nil, fmt.Errorf("cannot create instance: %v", &openstackError{err})
	}

	s := &openstackServer{
		p: p,
		d: openstackServerData{
			Id:                  server.Id,
			Name:                name,
			Flavor:              flavor.Name,
			Networks:            system.Networks,
			Status:              serverStatusProvisioning,
			Created:             time.Now(),
			DeleteOnTermination: deleteOnTermination,
		},

		system: system,
	}

	err = p.waitProvision(ctx, s)
	if err == nil {
		s.address, err = p.address(ctx, s)
	}

	if err == nil {
		err = p.waitServerBoot(ctx, s)
	}

	if err != nil {
		if p.removeMachine(ctx, s) != nil {
			return nil, &FatalError{fmt.Errorf("cannot allocate or deallocate (!) new Openstack instance %s: %v", s.d.Name, err)}
		}
		return nil, &FatalError{fmt.Errorf("cannot allocate new Openstack instance %s (%s): %v", s.d.Name, s.d.Id, err)}
	}

	return s, nil
}

func (p *openstackProvider) listServers() ([]*openstackServer, error) {
	debug("Listing available openstack instances...")

	filter := nova.NewFilter()
	servers, err := p.computeClient.ListServersDetail(filter)

	if err != nil {
		return nil, fmt.Errorf("cannot list openstack instances: %v", &openstackError{err})
	}

	var instances []*openstackServer
	for _, s := range servers {
		val, ok := s.Metadata["spread"]
		if ok && val == "true" {
			createdTime, err := time.Parse(time.RFC3339, s.Created)
			if err != nil {
				return nil, &FatalError{fmt.Errorf("cannot parse creation date for instances: %v", err)}
			}
			d := openstackServerData{
				Id:      s.Id,
				Name:    s.Name,
				Created: createdTime,
				Labels:  s.Metadata,
			}
			instances = append(instances, &openstackServer{p: p, d: d})
		}
	}
	return instances, nil
}

var openstackRemoveServerTimeout = 3 * time.Minute
var openstackRemoveRetry = 5 * time.Second

func (p *openstackProvider) removeMachine(ctx context.Context, s *openstackServer) error {
	if p == nil || p.computeClient == nil {
		return fmt.Errorf("The provider or compute client are not initialized")
	}

	if s == nil || s.d.Id == "" {
		return fmt.Errorf("The server pointer is not initialized")
	}

	_, err := p.computeClient.GetServer(s.d.Id)
	if err != nil {
		// this is when the server was already removed
		return nil
	}

	err = p.removeServer(ctx, s)
	if err != nil {
		return err
	}

	return nil
}

func (p *openstackProvider) removeServer(ctx context.Context, s *openstackServer) error {
	err := p.computeClient.DeleteServer(s.d.Id)
	if err != nil {
		return fmt.Errorf("cannot remove openstack instance: %v", &openstackError{err})
	}
	timeout := time.After(openstackRemoveServerTimeout)
	retry := time.NewTicker(openstackRemoveRetry)
	defer retry.Stop()

	for {
		server, err := p.computeClient.GetServer(s.d.Id)
		if err != nil || server.Status == nova.StatusDeleted {
			// this is when the server was already removed
			return nil
		}

		select {
		case <-retry.C:
		case <-timeout:
			printf("cannot remove the openstack server %s (%s): timeout reached with server status %s", server.Id, s.d.Name, server.Status)
			return nil
		}
	}
}

func (p *openstackProvider) GarbageCollect() error {
	if err := p.checkKey(); err != nil {
		return err
	}

	instances, err := p.listServers()
	if err != nil {
		return err
	}

	now := time.Now()
	haltTimeout := p.backend.HaltTimeout.Duration

	// Iterate over all the running instances
	for _, s := range instances {

		serverTimeout := haltTimeout
		if value, ok := s.d.Labels["halt-timeout"]; ok {
			d, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil {
				printf("WARNING: Ignoring bad openstack instances %s halt-timeout label: %q", s, value)
			} else {
				serverTimeout = d
			}
		}

		if serverTimeout == 0 {
			continue
		}

		printf("Checking openstack instance %s...", s)
		runningTime := now.Sub(s.d.Created)
		if runningTime > serverTimeout {
			printf("Server %s exceeds halt-timeout. Shutting it down...", s)
			err := p.removeMachine(context.Background(), s)
			if err != nil {
				printf("WARNING: Cannot garbage collect server %s: %v", s, err)
			}
		}
	}

	return nil
}

func (p *openstackProvider) checkKey() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.keyChecked {
		return p.keyErr
	}

	var err error

	if err == nil && p.computeClient == nil {

		// Load environment variables used to authenticate
		if p.backend.Key != "" {
			godotenv.Overload(p.backend.Key)
		}

		// retrieve variables used to authenticate from the environment
		cred, err := identity.CompleteCredentialsFromEnv()
		if err != nil {
			return &FatalError{fmt.Errorf("cannot retrieve credentials from env: %v", err)}
		}

		// Select the appropriate authentication method
		var authmode identity.AuthMode
		if os.Getenv("OS_ACCESS_KEY") != "" && os.Getenv("OS_SECRET_KEY") != "" {
			authmode = identity.AuthKeyPair
		} else if os.Getenv("OS_USERNAME") != "" && os.Getenv("OS_PASSWORD") != "" {
			authmode = identity.AuthUserPassV3
			if cred.Version > 0 && cred.Version != 3 {
				authmode = identity.AuthUserPass
			}
		} else {
			return &FatalError{fmt.Errorf("cannot determine authentication method to use")}
		}

		// Create auth client
		authClient := gooseclient.NewClient(cred, authmode, nil)
		err = authClient.Authenticate()
		if err != nil {
			return &FatalError{fmt.Errorf("cannot authenticate: %v", &openstackError{err})}
		}

		// Create clients for the used modules
		p.region = cred.Region
		p.osClient = authClient
		p.computeClient = nova.New(authClient)
		p.networkClient = neutron.New(authClient)
		p.imageClient = glance.New(authClient)
	}

	p.keyChecked = true
	p.keyErr = err
	return err
}
