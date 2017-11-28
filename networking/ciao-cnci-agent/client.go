//
// Copyright (c) 2016 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package main

import (
	"encoding/json"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/ciao-project/ciao/clogger/gloginterface"
	"github.com/ciao-project/ciao/networking/libsnnet"
	"github.com/ciao-project/ciao/payloads"
	"github.com/ciao-project/ciao/ssntp"
	"github.com/pkg/errors"

	"github.com/golang/glog"
)

var serverURL string
var serverCertPath string
var clientCertPath string
var computeNet string
var mgmtNet string
var enableNetwork bool
var enableNATssh bool
var agentUUID string

func init() {
	flag.StringVar(&serverURL, "server", "", "URL of SSNTP server, Use auto for auto discovery")
	flag.StringVar(&serverCertPath, "cacert", "/var/lib/ciao/CAcert-server-localhost.pem", "Client certificate")
	flag.StringVar(&clientCertPath, "cert", "/var/lib/ciao/cert-client-localhost.pem", "CA certificate")
	flag.StringVar(&computeNet, "compute-net", "", "Compute Subnet")
	flag.StringVar(&mgmtNet, "mgmt-net", "", "Management Subnet")
	flag.BoolVar(&enableNetwork, "network", true, "Enable networking")
	flag.BoolVar(&enableNATssh, "ssh", true, "Enable NAT and SSH")
	flag.StringVar(&agentUUID, "uuid", "", "UUID the CNCI Agent should use. Autogenerated otherwise")
}

const (
	lockDir       = "/tmp/lock/ciao"
	logDir        = "/var/lib/ciao/logs/cnci-agent"
	lockFile      = "cnci-agent.lock"
	interfacesDir = "/var/lib/ciao/network/interfaces"
)

var cnciRand io.Reader

type cmdWrapper struct {
	cmd interface{}
}
type statusConnected struct{}

type ssntpConn struct {
	sync.RWMutex
	ssntp.Client
	connected bool
}

func (s *ssntpConn) isConnected() bool {
	s.RLock()
	defer s.RUnlock()
	return s.connected
}

func (s *ssntpConn) setStatus(status bool) {
	s.Lock()
	s.connected = status
	s.Unlock()
}

type agentClient struct {
	ssntpConn
	db    *cnciDatabase
	cmdCh chan *cmdWrapper
}

func (client *agentClient) DisconnectNotify() {
	client.setStatus(false)
	glog.Warning("disconnected")
}

func (client *agentClient) ConnectNotify() {
	client.setStatus(true)
	client.cmdCh <- &cmdWrapper{&statusConnected{}}
	glog.Info("connected")
}

func (client *agentClient) StatusNotify(status ssntp.Status, frame *ssntp.Frame) {
	glog.Infof("STATUS %s", status)
}

func (client *agentClient) ErrorNotify(err ssntp.Error, frame *ssntp.Frame) {
	glog.Infof("ERROR %v", err)
}

func getLock() error {
	err := os.MkdirAll(lockDir, 0777)
	if err != nil {
		return errors.Wrapf(err, "unable to create lockdir %s", lockDir)
	}

	/* We're going to let the OS close and unlock this fd */
	lockPath := path.Join(lockDir, lockFile)
	fd, err := syscall.Open(lockPath, syscall.O_CREAT, syscall.S_IWUSR|syscall.S_IRUSR)
	if err != nil {
		return errors.Wrapf(err, "unable to open lock file %v", lockPath)
	}

	syscall.CloseOnExec(fd)

	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		errors.Wrapf(err, "cnci agent is already running. Exiting.")
	}

	return nil
}

/* Must be called after flag.Parse() */
func initLogger() error {
	logDirFlag := flag.Lookup("log_dir")
	if logDirFlag == nil {
		return errors.Errorf("log_dir does not exist")
	}

	if logDirFlag.Value.String() == "" {
		err := logDirFlag.Value.Set(logDir)
		if err != nil {
			return errors.Wrapf(err, "logger init")
		}
	}

	if err := os.MkdirAll(logDirFlag.Value.String(), 0755); err != nil {
		return errors.Wrapf(err, "unable to create log directory (%s)", logDir)
	}

	return nil
}

func createMandatoryDirs() error {
	if err := os.MkdirAll(interfacesDir, 0755); err != nil {
		return errors.Wrapf(err, "unable to create interfaces directory (%s)",
			interfacesDir)
	}
	return nil
}

func processRefreshCNCI(cmd *payloads.CommandCNCIRefresh) {
	c := &cmd.Command
	glog.Infof("Processing: CiaoCommandCNCIRefresh %v", c)

	// add call to function to refresh cnci.
	err := refreshCNCI(c)
	if err != nil {
		glog.Errorf("Unable to refresh CNCI list: %v", err)
	}
}

func processCommand(client *ssntpConn, cmd *cmdWrapper) {

	switch netCmd := cmd.cmd.(type) {

	case *payloads.EventTenantAdded:

		go func(cmd *cmdWrapper) {
			c := &netCmd.TenantAdded
			glog.Infof("Processing: CiaoEventTenantAdded %v", c)
			err := addRemoteSubnet(c)
			if err != nil {
				glog.Errorf("Error Processing: CiaoEventTenantAdded %+v", err)
			}
		}(cmd)

	case *payloads.EventTenantRemoved:

		go func(cmd *cmdWrapper) {
			c := &netCmd.TenantRemoved
			glog.Infof("Processing: CiaoEventTenantRemoved %v", c)
			err := delRemoteSubnet(c)

			if err != nil {
				glog.Errorf("Error Processing: CiaoEventTenantRemoved %+v", err)
			}
		}(cmd)

	case *payloads.CommandAssignPublicIP:

		go func(cmd *cmdWrapper) {
			c := &netCmd.AssignIP
			glog.Infof("Processing: CiaoCommandAssignPublicIP %v", c)
			err := assignPubIP(c)
			if err != nil {
				glog.Errorf("Error Processing: CiaoCommandAssignPublicIP %+v", err)
				err = sendNetworkError(client, ssntp.AssignPublicIPFailure, c)
			} else {
				err = sendNetworkEvent(client, ssntp.PublicIPAssigned, c)
			}

			if err != nil {
				glog.Errorf("Unable to send event : %+v", err)
			}
		}(cmd)

	case *payloads.CommandReleasePublicIP:

		go func(cmd *cmdWrapper) {
			c := &netCmd.ReleaseIP
			glog.Infof("Processing: CiaoCommandReleasePublicIP %v", c)
			err := releasePubIP(c)
			if err != nil {
				glog.Errorf("Error Processing: CiaoCommandReleasePublicIP %+v", c)
				err = sendNetworkError(client, ssntp.UnassignPublicIPFailure, c)
			} else {
				err = sendNetworkEvent(client, ssntp.PublicIPUnassigned, c)
			}

			if err != nil {
				glog.Errorf("Unable to send event : %+v", err)
			}
		}(cmd)

	case *payloads.CommandCNCIRefresh:

		go processRefreshCNCI(netCmd)

	case *statusConnected:
		//Block and send this as it does not make sense to send other events
		//or process commands when we have not yet registered
		glog.Infof("Processing: status connected")
		err := sendNetworkEvent(client, ssntp.ConcentratorInstanceAdded, nil)
		if err != nil {
			glog.Errorf("Unable to register : %+v", err)
		}

	default:
		glog.Errorf("Processing unknown command")

	}
}

func (client *agentClient) CommandNotify(cmd ssntp.Command, frame *ssntp.Frame) {
	payload := frame.Payload

	switch cmd {
	case ssntp.AssignPublicIP:
		glog.Infof("CMD: ssntp.AssignPublicIP %v", len(payload))

		go func(payload []byte) {
			var assignIP payloads.CommandAssignPublicIP
			err := yaml.Unmarshal(payload, &assignIP)
			if err != nil {
				glog.Warning("Error unmarshalling StartFailure")
				return
			}
			glog.Infof("EVENT: ssntp.AssignPublicIP %v", assignIP)

			err = dbProcessCommand(client.db, &assignIP)
			if err != nil {
				glog.Errorf("unable to save state %+v", err)
			}

			client.cmdCh <- &cmdWrapper{&assignIP}
		}(payload)

	case ssntp.ReleasePublicIP:
		glog.Infof("CMD: ssntp.ReleasePublicIP %v", len(payload))

		go func(payload []byte) {
			var releaseIP payloads.CommandReleasePublicIP
			err := yaml.Unmarshal(payload, &releaseIP)
			if err != nil {
				glog.Warning("Error unmarshalling StartFailure")
				return
			}
			glog.Infof("EVENT: ssntp.ReleasePublicIP %s", releaseIP)

			err = dbProcessCommand(client.db, &releaseIP)
			if err != nil {
				glog.Errorf("unable to save state %+v", err)
			}

			client.cmdCh <- &cmdWrapper{&releaseIP}
		}(payload)

	case ssntp.RefreshCNCI:
		glog.Infof("CMD: ssntp.RefreshCNCI %v", len(payload))

		go func(payload []byte) {
			var refreshCNCI payloads.CommandCNCIRefresh

			err := yaml.Unmarshal(payload, &refreshCNCI)
			if err != nil {
				glog.Warning("Error unmarshalling CNCI refresh")
				return
			}
			glog.Infof("CMD: ssntp.RefreshCNCI %v", refreshCNCI)

			client.cmdCh <- &cmdWrapper{&refreshCNCI}
		}(payload)

	default:
		glog.Infof("CMD: %s", cmd)
	}
}

func (client *agentClient) EventNotify(event ssntp.Event, frame *ssntp.Frame) {
	payload := frame.Payload

	switch event {
	case ssntp.TenantAdded:
		glog.Infof("EVENT: ssntp.TenantAdded %v", len(payload))

		go func(payload []byte) {
			var tenantAdded payloads.EventTenantAdded
			err := yaml.Unmarshal(payload, &tenantAdded)
			if err != nil {
				glog.Warning("Error unmarshalling StartFailure")
				return
			}
			glog.Infof("EVENT: ssntp.TenantAdded %s", tenantAdded)

			err = dbProcessCommand(client.db, &tenantAdded)
			if err != nil {
				glog.Errorf("unable to save state %+v", err)
			}

			client.cmdCh <- &cmdWrapper{&tenantAdded}
		}(payload)

	case ssntp.TenantRemoved:
		glog.Infof("EVENT: ssntp.TenantRemoved %v", len(payload))

		go func(payload []byte) {
			var tenantRemoved payloads.EventTenantRemoved
			err := yaml.Unmarshal(payload, &tenantRemoved)
			if err != nil {
				glog.Warning("Error unmarshalling StartFailure")
				return
			}
			glog.Infof("EVENT: ssntp.TenantRemoved %s", tenantRemoved)

			err = dbProcessCommand(client.db, &tenantRemoved)
			if err != nil {
				glog.Errorf("unable to save state %+v", err)
			}

			client.cmdCh <- &cmdWrapper{&tenantRemoved}
		}(payload)

	default:
		glog.Infof("EVENT %s", event)
	}
}

func connectToServer(db *cnciDatabase, doneCh chan struct{}, statusCh chan struct{}) {

	defer func() {
		statusCh <- struct{}{}
	}()

	cfg := &ssntp.Config{UUID: agentUUID, URI: serverURL, CAcert: serverCertPath, Cert: clientCertPath,
		Log: ssntp.Log, Rand: cnciRand}
	client := &agentClient{db: db, cmdCh: make(chan *cmdWrapper)}

	dialCh := make(chan error)

	go func() {
		err := client.Dial(cfg, client)
		if err != nil {
			glog.Errorf("Unable to connect to server %v", err)
			dialCh <- err
			return
		}

		dialCh <- err
	}()

	dialing := true

DONE:
	for {
		select {
		case err := <-dialCh:
			dialing = false
			if err != nil {
				break DONE
			}
		case <-doneCh:
			client.Close()
			if !dialing {
				break DONE
			}
		case cmd := <-client.cmdCh:
			/*
				Double check we're not quitting here.  Otherwise a flood of commands
				from the server could block our exit for an arbitrary amount of time,
				i.e, doneCh and cmdCh could become available at the same time.
			*/
			select {
			case <-doneCh:
				client.Close()
				break DONE
			default:
			}
			glog.Infof("cmd channel: %v", cmd)
			processCommand(&client.ssntpConn, cmd)
		}
	}
}

//Try to discover the scheduler automatically if needed
func discoverScheduler() error {

	if serverURL != "auto" {
		return nil
	}

	serverURL = ""
	return nil

}

//CloudInitJSON represents the contents of the cloud init file
type CloudInitJSON struct {
	UUID     string `json:"uuid"`
	Hostname string `json:"hostname"`
}

//Try to discover the UUID automatically if needed
func discoverUUID() (string, error) {

	//TODO: Do this via systemd
	out, err := exec.Command("mount", "/dev/vdb", "/media").Output()
	if err != nil {
		//Ignore this error, we may be already mounted
		glog.Errorf("Unable to mount /dev/vdb %v %s", err, string(out))
	}

	payload, err := ioutil.ReadFile("/media/openstack/latest/meta_data.json")
	if err != nil {
		return "", errors.Wrapf(err, "Unable to read /media/openstack/latest/meta_data.json %v")
	}

	metaData := &CloudInitJSON{}
	err = json.Unmarshal(payload, metaData)
	if err != nil {
		return "", errors.Wrapf(err, "Unable to read UUID from /media/openstack/latest/meta_data.json")
	}

	return metaData.UUID, nil
}

//Rebuild network state from database
func rebuildNetworkState(db *cnciDatabase) error {
	var lastError error
	if db == nil {
		return nil
	}

	db.SubnetMap.Lock()
	defer db.SubnetMap.Unlock()
	db.PublicIPMap.Lock()
	defer db.PublicIPMap.Unlock()

	for key, subnet := range db.SubnetMap.m {
		glog.Infof("Key: %v Subnet: %v", key, subnet)
		err := addRemoteSubnet(subnet)
		if err != nil {
			lastError = err
			glog.Errorf("rebuildNetworkState: %v", err)
		}
	}

	for key, publicIP := range db.PublicIPMap.m {
		glog.Infof("Key: %v PublicIP: %v", key, publicIP)
		err := assignPubIP(publicIP)
		if err != nil {
			lastError = err
			glog.Errorf("rebuildNetworkState: %v", err)
		}
	}

	return errors.Wrapf(lastError, "rebuild network state")
}

func main() {

	if getLock() != nil {
		os.Exit(1)
	}

	flag.Parse()

	libsnnet.Logger = gloginterface.CiaoGlogLogger{}

	if err := initLogger(); err != nil {
		log.Fatalf("Unable to initialise logs: %+v", err)
	}

	glog.Info("Starting CNCI Agent")

	if err := createMandatoryDirs(); err != nil {
		glog.Fatalf("Unable to create mandatory dirs: %+v", err)
	}

	if err := discoverScheduler(); err != nil {
		glog.Fatalf("Unable to auto discover scheduler: %+v", err)
	}
	glog.Errorf("Scheduler address %v", serverURL)

	if agentUUID == "" {
		agentUUID, _ = discoverUUID()
	}
	glog.Errorf("CNCI Agent: UUID : %v", agentUUID)

	doneCh := make(chan struct{})
	statusCh := make(chan struct{})
	signalCh := make(chan os.Signal, 1)
	timeoutCh := make(chan struct{})
	wdogCh := make(chan struct{})
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	//TODO: Wait till the node gets an IP address before we kick this off
	//TODO: Add a IP address change notifier to handle potential IP address change
	if err := initNetwork(signalCh); err != nil {
		glog.Fatalf("Unable to setup network. %+v", err)
	}

	//Recover the state from the database and then
	//recreate the CNCI state by replaying the commands
	//Has to be done prior to accepting commands over the network
	db, err := dbInit()
	if err != nil {
		glog.Fatalf("Unable to setup database. %+v", err)
	}

	if err := rebuildNetworkState(db); err != nil {
		glog.Errorf("Unable to rebuild network state. %+v", err)
	}

	go connectToServer(db, doneCh, statusCh)

	//Prime the watchdog
	go func() {
		wdogCh <- struct{}{}
	}()

DONE:
	for {
		select {
		case <-signalCh:
			glog.Info("Received terminating signal.  Waiting for server loop to quit")
			close(doneCh)
			go func() {
				time.Sleep(time.Second)
				timeoutCh <- struct{}{}
			}()
		case <-statusCh:
			glog.Info("Server Loop quit cleanly")
			break DONE
		case <-timeoutCh:
			glog.Warning("Server Loop did not exit within 1 second quitting")
			break DONE
		case <-wdogCh:
			glog.Info("Watchdog kicker")
			go func() {
				//TODO: Add software watchdog to CNCI VM
				time.Sleep(5 * time.Second)
				wdogCh <- struct{}{}
			}()
		}
	}

	glog.Flush()
	glog.Info("Exit")
}
