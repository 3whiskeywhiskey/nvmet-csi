package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/test/mock"
)

// Suite-level variables
var (
	testRunID        string
	mockRDS          *mock.MockRDSServer
	driverEndpoint   string
	grpcConn         *grpc.ClientConn
	identityClient   csi.IdentityClient
	controllerClient csi.ControllerClient
	nodeClient       csi.NodeClient
	ctx              context.Context
	cancel           context.CancelFunc
)

// TestE2E is the entry point for the Ginkgo test suite
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "nvmet-csi driver E2E Suite")
}

var _ = BeforeSuite(func() {
	// Setup logging
	klog.SetOutput(GinkgoWriter)

	// Generate unique test run ID for this test execution
	testRunID = fmt.Sprintf("e2e-%d", time.Now().Unix())
	klog.Infof("Starting E2E test suite with testRunID=%s", testRunID)

	// Start mock RDS server on random port (port 0)
	By("Starting mock RDS server")
	var err error
	mockRDS, err = mock.NewMockRDSServer(0)
	Expect(err).NotTo(HaveOccurred(), "Failed to create mock RDS server")

	err = mockRDS.Start()
	Expect(err).NotTo(HaveOccurred(), "Failed to start mock RDS server")

	klog.Infof("Mock RDS server started on %s:%d", mockRDS.Address(), mockRDS.Port())

	key, keyErr := mock.GenerateClientPrivateKey()
	Expect(keyErr).NotTo(HaveOccurred(), "Failed to generate test SSH key")

	// Create driver with both controller and node enabled
	By("Creating CSI driver")
	driverConfig := driver.DriverConfig{
		DriverName:            "nvmet.csi.whiskey.works",
		Version:               "test",
		NodeID:                "test-node-1",
		RDSAddress:            mockRDS.Address(),
		RDSPort:               mockRDS.Port(),
		RDSUser:               "admin",
		RDSPrivateKey:         key,
		RDSInsecureSkipVerify: true,
		RDSVolumeBasePath:     testVolumeBasePath,
		ManagedNQNPrefix:      "nqn.2000-02.com.mikrotik:",
		EnableController:      true,
		EnableNode:            true,
		K8sClient:             nil,
		Metrics:               nil,
	}

	drv, err := driver.NewDriver(driverConfig)
	Expect(err).NotTo(HaveOccurred(), "Failed to create driver")

	// Setup Unix socket path
	driverEndpoint = fmt.Sprintf("/tmp/csi-e2e-%s.sock", testRunID)
	_ = os.Remove(driverEndpoint) // Clean up any existing socket

	// Start driver in background goroutine
	By("Starting CSI driver")
	endpoint := fmt.Sprintf("unix://%s", driverEndpoint)
	go func() {
		defer GinkgoRecover()
		if err := drv.Run(endpoint); err != nil {
			klog.Infof("Driver stopped: %v", err)
		}
	}()

	// Wait for socket to be ready using Eventually
	By("Waiting for CSI socket to be ready")
	Eventually(func() bool {
		if _, err := os.Stat(driverEndpoint); err != nil {
			return false
		}
		// Try to connect to verify it's accepting connections
		conn, err := net.Dial("unix", driverEndpoint)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue(), "CSI socket should be ready")

	klog.Infof("CSI socket ready at %s", driverEndpoint)

	// Create gRPC connection and clients
	By("Creating gRPC clients")
	grpcConn, err = grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	Expect(err).NotTo(HaveOccurred(), "Failed to create gRPC connection")

	identityClient = csi.NewIdentityClient(grpcConn)
	controllerClient = csi.NewControllerClient(grpcConn)
	nodeClient = csi.NewNodeClient(grpcConn)

	// Create context with timeout for all tests
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)

	klog.Infof("E2E suite setup complete")
})

var _ = AfterSuite(func() {
	By("Cleaning up test suite")

	// Clean up any volumes created during tests with testRunID prefix
	if controllerClient != nil && ctx != nil {
		By("Deleting test volumes")
		listResp, err := controllerClient.ListVolumes(ctx, &csi.ListVolumesRequest{})
		if err == nil {
			for _, entry := range listResp.Entries {
				volumeID := entry.Volume.VolumeId
				// Delete volumes created by this test run.
				if isTestVolumeName(volumeID) {
					klog.Infof("Cleaning up test volume: %s", volumeID)
					_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{
						VolumeId: volumeID,
					})
				}
			}
		}
	}

	// Close gRPC connection
	if grpcConn != nil {
		By("Closing gRPC connection")
		_ = grpcConn.Close()
	}

	// Cancel context
	if cancel != nil {
		cancel()
	}

	// Stop mock RDS server
	if mockRDS != nil {
		By("Stopping mock RDS server")
		err := mockRDS.Stop()
		Expect(err).NotTo(HaveOccurred(), "Failed to stop mock RDS server")
	}

	// Remove socket file
	if driverEndpoint != "" {
		By("Removing socket file")
		_ = os.Remove(driverEndpoint)
	}

	klog.Infof("E2E suite cleanup complete")
})

var _ = Describe("E2E Suite Sanity", func() {
	It("should have valid test infrastructure", func() {
		Expect(testRunID).NotTo(BeEmpty(), "testRunID should be set")
		Expect(mockRDS).NotTo(BeNil(), "mockRDS should be initialized")
		Expect(controllerClient).NotTo(BeNil(), "controllerClient should be initialized")
		klog.Infof("E2E suite infrastructure validated, testRunID=%s", testRunID)
	})
})
