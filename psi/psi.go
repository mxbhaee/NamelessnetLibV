package psi

// This package is a shim between Java/Obj-C and the "psiphon" package. Due to limitations
// on what Go types may be exposed (http://godoc.org/golang.org/x/mobile/cmd/gobind),
// a psiphon.Controller cannot be directly used by Java. This shim exposes a trivial
// Start/Stop interface on top of a single Controller instance.

import (
	"fmt"
	"sync"

	"os"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon"
	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common"
	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/protocol"
	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/tun"
)

type PsiphonProvider interface {
	Notice(noticeJSON string)
	HasNetworkConnectivity() int
	BindToDevice(fileDescriptor int) error
	IPv6Synthesize(IPv4Addr string) string
	GetPrimaryDnsServer() string
	GetSecondaryDnsServer() string
}

var controllerMutex sync.Mutex
var controller *psiphon.Controller
var shutdownBroadcast chan struct{}
var controllerWaitGroup *sync.WaitGroup

func Start(
	configJson, embeddedServerEntryList,
	embeddedServerEntryListPath string,
	provider PsiphonProvider,
	useDeviceBinder bool, useIPv6Synthesizer bool) error {

	controllerMutex.Lock()
	defer controllerMutex.Unlock()

	if controller != nil {
		return fmt.Errorf("already started")
	}

	config, err := psiphon.LoadConfig([]byte(configJson))
	if err != nil {
		return fmt.Errorf("error loading configuration file: %s", err)
	}
	config.NetworkConnectivityChecker = provider

	if useDeviceBinder {
		config.DeviceBinder = provider
		config.DnsServerGetter = provider
	}

	if useIPv6Synthesizer {
		config.IPv6Synthesizer = provider
	}

	psiphon.SetNoticeOutput(psiphon.NewNoticeReceiver(
		func(notice []byte) {
			provider.Notice(string(notice))
		}))

	psiphon.NoticeBuildInfo()

	// TODO: should following errors be Notices?

	err = psiphon.InitDataStore(config)
	if err != nil {
		return fmt.Errorf("error initializing datastore: %s", err)
	}

	// Stores list of server entries.
	err = storeServerEntries(embeddedServerEntryListPath, embeddedServerEntryList)
	if err != nil {
		return err
	}

	controller, err = psiphon.NewController(config)
	if err != nil {
		return fmt.Errorf("error initializing controller: %s", err)
	}

	shutdownBroadcast = make(chan struct{})
	controllerWaitGroup = new(sync.WaitGroup)
	controllerWaitGroup.Add(1)
	go func() {
		defer controllerWaitGroup.Done()
		controller.Run(shutdownBroadcast)
	}()

	return nil
}

func Stop() {

	controllerMutex.Lock()
	defer controllerMutex.Unlock()

	if controller != nil {
		close(shutdownBroadcast)
		controllerWaitGroup.Wait()
		controller = nil
		shutdownBroadcast = nil
		controllerWaitGroup = nil
	}
}

// SetClientVerificationPayload is a passthrough to
// Controller.SetClientVerificationPayloadForActiveTunnels.
// Note: should only be called after Start() and before Stop(); otherwise,
// will silently take no action.
func SetClientVerificationPayload(clientVerificationPayload string) {

	controllerMutex.Lock()
	defer controllerMutex.Unlock()

	if controller != nil {
		controller.SetClientVerificationPayloadForActiveTunnels(clientVerificationPayload)
	}
}

// Encrypt and upload feedback.
func SendFeedback(configJson, diagnosticsJson, b64EncodedPublicKey, uploadServer, uploadPath, uploadServerHeaders string) {
	err := psiphon.SendFeedback(configJson, diagnosticsJson, b64EncodedPublicKey, uploadServer, uploadPath, uploadServerHeaders)
	if err != nil {
		psiphon.NoticeAlert("Failed to upload feedback: %s", err)
	} else {
		psiphon.NoticeInfo("Feedback uploaded successfully")
	}
}

func GetPacketTunnelMTU() int {
	return tun.DEFAULT_MTU
}

func GetPacketTunnelDNSResolverIPv4Address() string {
	return tun.GetTransparentDNSResolverIPv4Address().String()
}

func GetPacketTunnelDNSResolverIPv6Address() string {
	return tun.GetTransparentDNSResolverIPv6Address().String()
}

// Helper function to store a list of server entries.
// if embeddedServerEntryListPath is not empty, embeddedServerEntryList will be ignored.
func storeServerEntries(embeddedServerEntryListPath, embeddedServerEntryList string) error {

	// if embeddedServerEntryListPath is not empty, ignore embeddedServerEntryList.
	if embeddedServerEntryListPath != "" {

		serverEntriesFile, err := os.Open(embeddedServerEntryListPath)
		if err != nil {
			return fmt.Errorf("failed to read remote server list: %s", common.ContextError(err))
		}
		defer serverEntriesFile.Close()

		err = psiphon.StreamingStoreServerEntriesWithIOReader(serverEntriesFile, protocol.SERVER_ENTRY_SOURCE_EMBEDDED)
		if err != nil {
			return fmt.Errorf("failed to store common remote server list: %s", common.ContextError(err))
		}

	} else {

		serverEntries, err := protocol.DecodeServerEntryList(
			embeddedServerEntryList,
			common.GetCurrentTimestamp(),
			protocol.SERVER_ENTRY_SOURCE_EMBEDDED)
		if err != nil {
			return fmt.Errorf("error decoding embedded server entry list: %s", err)
		}
		err = psiphon.StoreServerEntries(serverEntries, false)
		if err != nil {
			return fmt.Errorf("error storing embedded server entry list: %s", err)
		}
	}

	return nil
}
