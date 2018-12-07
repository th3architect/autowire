package main

import (
  "fmt"       // A package in the Go standard library.
  "net"
  "log"
  "os"
  "io/ioutil"
  "time"
  "strconv"
  "strings"
  "github.com/hashicorp/consul/api"
  "github.com/geniousphp/autowire/wireguard"
  "github.com/geniousphp/autowire/wg_quick"
  "github.com/geniousphp/autowire/ifconfig"
)

var kvPrefix = "autowire"
var wgInterfaceName = "wg0"
var longKvPrefix = kvPrefix + "/" + wgInterfaceName + "/"

var wgConfigFolder = "/etc/autowire"
var interfaceName = "enp0s8"
var wgRange = "192.168.200.0/24"
var wgPort = 51820

func main() {



  privKey, pubKey, err := initWgKeys(wgConfigFolder + "/" + wgInterfaceName)
  if err != nil {
    log.Fatal(err)
  }
  log.Println(privKey)
  log.Println(pubKey)

  physicalIpAddr, err := getPhysicalIpAddr(interfaceName)
  if err != nil {
    log.Fatal(err)
  }
  if physicalIpAddr == "" {
    log.Fatal("Error while detecting network interface or Ip Address")
  }

  conf := api.DefaultConfig()
  ConsulClient, err := api.NewClient(conf)
  if err != nil {
    log.Fatal(err)
  }
  err = initialize(ConsulClient, physicalIpAddr, privKey, pubKey)
  if err != nil {
    log.Fatal(err)
  }



}


func initWgKeys(wgInterfaceConfigFolder string) (string, string, error) {
  if _, err := os.Stat(wgInterfaceConfigFolder + "/private"); os.IsNotExist(err) {
    err := os.MkdirAll(wgInterfaceConfigFolder, 0700)

    if err != nil {
      return "", "", err
    }

    privKey, err := wireguard.Genkey()
    if err != nil {
      return "", "", err
    }

    err = ioutil.WriteFile(wgInterfaceConfigFolder + "/private", privKey, 0600)
    if err != nil {
      return "", "", err
    }

  }

  privKey, err := ioutil.ReadFile(wgInterfaceConfigFolder + "/private")
  if err != nil {
    return "", "", err
  }

  pubKey, err := wireguard.ExtractPubKey(privKey)
  if err != nil {
    return "", "", err
  }

  return strings.TrimSuffix(string(privKey[:]), "\n"), strings.TrimSuffix(string(pubKey[:]), "\n"), nil
  // return privKey, pubKey, nil
}

func getPhysicalIpAddr(interfaceName string) (string, error) {
  if(interfaceName == ""){
    // TODO: If interfaceName is empty, return the first address of the first interface
    return "", nil
  }
  inet, err := ifconfig.GetIpOfIf(interfaceName)
  if err != nil {
    return "", err
  }

  ipAddr, _, err := net.ParseCIDR(inet)
  if err != nil {
    return "", err
  }
  return ipAddr.String(), nil
}



func initialize(ConsulClient *api.Client, physicalIpAddr string, privKey string, pubKey string) error{
  _, wgIpNet, err := net.ParseCIDR(wgRange)
  if err != nil {
    return err
  }

  var ConsulKV *api.KV
  ConsulKV = ConsulClient.KV()

  kvpairsWgRange, _, err := ConsulKV.Get(longKvPrefix + "range", nil)
  if err != nil {
    return err
  }
  if kvpairsWgRange == nil || string(kvpairsWgRange.Value[:]) != wgRange {
    log.Println("The wireguard IP range doesn't exist, willing to create it right now")
    _, err := ConsulKV.Put(&api.KVPair{Key: longKvPrefix + "range", Value: []byte(wgRange)}, nil)
    if err != nil {
      return err
    }

  }

  kvpair, _, err := ConsulKV.Get(longKvPrefix + "nodes/" + physicalIpAddr + "/ip", nil)
  if err != nil {
    return err
  }

  if kvpair != nil {
    myPickedWgAddr := string(kvpair.Value[:])
    fmt.Println("I already picked wg ip and registred it into KV", myPickedWgAddr)

    if(wgIpNet.Contains(net.ParseIP(myPickedWgAddr))){
      fmt.Println("My picked wg ip fit in the range")

      started, err := ifconfig.IsInterfaceStarted(wgInterfaceName)
      if err != nil {
        return err
      }
      maskBits, _ := wgIpNet.Mask.Size()
      // newWgInterface := wireguard.Interface{wgInterfaceName, fmt.Sprintf("%s/%d", myPickedWgAddr, maskBits), wgPort, privKey}
      newWGConfig := wireguard.WGConfig{
        Interface: wireguard.Interface{
          Name: wgInterfaceName, 
          Address: fmt.Sprintf("%s/%d", myPickedWgAddr, maskBits), 
          ListenPort: wgPort, 
          PrivateKey: privKey,
        },
        Peers: make(map[string]wireguard.Peer),
      } 
      if(started){
        fmt.Println("I already started my wg interface")

        if(wireguard.IsWgInterfaceWellConfigured(newWGConfig)){
          fmt.Println("My interface is well configured")
          monitorPeers(ConsulClient, physicalIpAddr)
        } else {
          fmt.Println("My interface is not well configured")
          wg_quick.StopInterface(wgInterfaceName)
          return initialize(ConsulClient, physicalIpAddr, privKey, pubKey)
        }


      } else {
        fmt.Println("Will bring up my wg interface")
        wireguard.ConfigureInterface(newWGConfig)
        wg_quick.StartInterface(wgInterfaceName)
        return initialize(ConsulClient, physicalIpAddr, privKey, pubKey)
      }


    } else {
      fmt.Println("My picked wg ip out of range")
      _, err := ConsulKV.DeleteTree(longKvPrefix + "nodes/" + physicalIpAddr, nil)
      if err != nil {
        return err
      }
      return initialize(ConsulClient, physicalIpAddr, privKey, pubKey)
    }
    

  } else {
    fmt.Println("I didn't yet picked an IP from RANGE")

    opts := &api.LockOptions{
      Key:        longKvPrefix + "pick-ip-lock",
      Value:      []byte(physicalIpAddr),
      SessionOpts: &api.SessionEntry{
        Behavior: "release",
        TTL: "10s",
      },
    }
    lock, err := ConsulClient.LockOpts(opts)
    if err != nil {
      return err
    }

    stopCh := make(chan struct{})
    _, err = lock.Lock(stopCh)
    if err != nil {
      return err
    }
    //Resource locked


    //get all the picked ip
    kvpairsNodes, _, err := ConsulKV.List(longKvPrefix + "nodes", &api.QueryOptions{AllowStale: false, RequireConsistent: true, UseCache: false})
    if err != nil {
      return err
    }
    var usedWgIps []string
    for _, kvpNode := range kvpairsNodes {
      usedWgIps = append(usedWgIps, string(kvpNode.Value[:]))
    }


    wgIpStart := wgIpNet.IP
    //let's pick spare ip
    incIp(wgIpStart) //Skip IP Network

    //The loop goes over all ips in the network
    for myFutureWgIp := wgIpStart; wgIpNet.Contains(myFutureWgIp); incIp(myFutureWgIp) {
      if(contains(usedWgIps, myFutureWgIp.String())){
        fmt.Println(myFutureWgIp.String(), "exist, skipping...")
      } else {
        fmt.Println("Found IP", myFutureWgIp)
        //save it to /wireguard/wg0/nodes/physicalIpAddr
        
        nodeKVTxnOps := api.KVTxnOps{
          &api.KVTxnOp{
            Verb:    api.KVSet,
            Key:     longKvPrefix + "nodes/" + physicalIpAddr + "/ip",
            Value:   []byte(myFutureWgIp.String()),
          },
          &api.KVTxnOp{
            Verb:    api.KVSet,
            Key:     longKvPrefix + "nodes/" + physicalIpAddr + "/pubKey",
            Value:   []byte(pubKey),
          },
          &api.KVTxnOp{
            Verb:    api.KVSet,
            Key:     longKvPrefix + "nodes/" + physicalIpAddr + "/port",
            Value:   []byte(strconv.Itoa(wgPort)),
          },
          &api.KVTxnOp{
            Verb:    api.KVSet,
            Key:     longKvPrefix + "nodes/" + physicalIpAddr + "/allowedips",
            Value:   []byte(myFutureWgIp.String() + "/32"),
          },
        }
        ok, _, _, err := ConsulKV.Txn(nodeKVTxnOps, nil)
        lock.Unlock()  //Unlock Resource
        if err != nil {
          return err
        }
        if !ok {
          return fmt.Errorf("Transaction was rolled back")
        }

        // TODO: Check that ip we didn't pick broadcast IP
        // Check if there is no free ip left
        // if(contains(usedWgIps, myFutureWgIp.String())){
        //   return fmt.Errorf("There is no spare IP left in %s CIDR", wgRange)
        // }

        return initialize(ConsulClient, physicalIpAddr, privKey, pubKey)

        break;
      }
    }
  }


  return nil
}


func monitorPeers(ConsulClient *api.Client, physicalIpAddr string) {
  peers := make(map[string]map[string]string)
  stopMonitorKvPrefixchan := make(chan bool)
  stopMonitorNodesChan := make(chan bool)
  newPeerschan := make(chan map[string]map[string]string)
  newNodesChan := make(chan map[string]string)
  go monitorKvPrefix(ConsulClient, newPeerschan, stopMonitorKvPrefixchan)
  go monitorNodes(ConsulClient, physicalIpAddr, newNodesChan, stopMonitorNodesChan)

  for {
    select {
      case <-stopMonitorKvPrefixchan:
        fmt.Println("monitorKvPrefix goroutine stopped")
      case newPeers := <-newPeerschan:
        fmt.Println("received new peers from monitorKvPrefix goroutine")
        configureWgPeers(physicalIpAddr, peers, newPeers)
      case <-stopMonitorNodesChan:
        fmt.Println("monitorNodes goroutine stopped")
      case nodesPhysicalIpAddr := <-newNodesChan:
        fmt.Println("received new nodes")
        removeLeftNodes(ConsulClient, peers, nodesPhysicalIpAddr)
        printPeersMap(peers)

    }
  }
  
}

func monitorKvPrefix(ConsulClient *api.Client, newPeerschan chan map[string]map[string]string, stopMonitorKvPrefixChan chan bool) {
  var ConsulKV *api.KV
  ConsulKV = ConsulClient.KV()

  newPeers := make(map[string]map[string]string)

  var waitIndex uint64
  waitIndex = 0

  for {
    opts := api.QueryOptions{
      AllowStale: false, 
      RequireConsistent: true, 
      UseCache: false,
      WaitIndex: waitIndex,
    }
    fmt.Println("Will watch consul kv prefix in blocking query now", waitIndex)
    kvpairsNodes, meta, err := ConsulKV.List(longKvPrefix + "nodes", &opts)
    if err != nil {
      // Prevent backend errors from consuming all resources.
      log.Fatal(err)
      time.Sleep(time.Second * 2)
      continue
    }
    for _, kvpNode := range kvpairsNodes {
      physicalIpAddr := strings.Split(kvpNode.Key, "/")[3]
      field := strings.Split(kvpNode.Key, "/")[4]
      value := string(kvpNode.Value[:])
      if _, ok := newPeers[physicalIpAddr]; !ok {
        newPeers[physicalIpAddr] = make(map[string]string)
        newPeers[physicalIpAddr]["endpoint"] = physicalIpAddr
      }
      newPeers[physicalIpAddr][field] = value
    }
    newPeerschan <- newPeers

    waitIndex = meta.LastIndex
  }
  stopMonitorKvPrefixChan <- true
}

func configureWgPeers(myPhysicalIpAddr string, peers map[string]map[string]string, newPeers map[string]map[string]string) {

  for physicalIpAddrKey, peer := range peers {
    // peer doesn't exit in newPeers anymore
    if _, ok := newPeers[physicalIpAddrKey]; !ok {
      fmt.Println("Removing a peer that doesn't exist anymore")
      wireguard.RemovePeer(wgInterfaceName, peer)
      delete(peers, physicalIpAddrKey)
    } else {
      if(peer["pubKey"] != newPeers[physicalIpAddrKey]["pubKey"]){
        fmt.Println("Reconfiguring a Peer that has same endpoint and different public key")
        wireguard.RemovePeer(wgInterfaceName, peer)
        delete(peers, physicalIpAddrKey)
        wireguard.ConfigurePeer(wgInterfaceName, peer)
        peers[physicalIpAddrKey] = peer
      } else {
        if(peer["allowedips"] != newPeers[physicalIpAddrKey]["allowedips"] || 
          peer["port"] != newPeers[physicalIpAddrKey]["port"] || 
          peer["endpoint"] != newPeers[physicalIpAddrKey]["endpoint"]){

          fmt.Println("Reconfiguring a Peer that changes its params")
          wireguard.ConfigurePeer(wgInterfaceName, peer)
          peers[physicalIpAddrKey]["allowedips"] = newPeers[physicalIpAddrKey]["allowedips"]
          peers[physicalIpAddrKey]["port"] = newPeers[physicalIpAddrKey]["port"]
          peers[physicalIpAddrKey]["endpoint"] = newPeers[physicalIpAddrKey]["endpoint"]
          
        }
      }
    }
  }


  for physicalIpAddrKey, peer := range newPeers {
    // If physicalIpAddrKey is my ip, skip it
    if(myPhysicalIpAddr == physicalIpAddrKey){
      continue
    }

    // new peer doesn't exist in peers
    if _, ok := peers[physicalIpAddrKey]; !ok {
      fmt.Println("Adding New Peer")
      wireguard.ConfigurePeer(wgInterfaceName, peer)
      peers[physicalIpAddrKey] = peer
    }
  }

}

func printPeersMap(peers map[string]map[string]string) {
  for physicalIpAddrKey, peer := range peers {
    for key, value := range peer {
      fmt.Println(physicalIpAddrKey, key, value)
    }
  }
}

func monitorNodes(ConsulClient *api.Client, physicalIpAddr string, newNodesChan chan map[string]string, stopMonitorNodesChan chan bool) {
  opts := &api.LockOptions{
    Key:        longKvPrefix + "monitor-nodes-lock",
    Value:      []byte(physicalIpAddr),
    SessionOpts: &api.SessionEntry{
      Behavior: "release",
      TTL: "10s",
    },
  }
  lock, err := ConsulClient.LockOpts(opts)
  if err != nil {
    log.Fatal(err)
  }
  stopCh := make(chan struct{})
  _, err = lock.Lock(stopCh)
  if err != nil {
    log.Fatal(err)
  }

  var ConsulCatalog *api.Catalog
  ConsulCatalog = ConsulClient.Catalog()
  var waitIndex uint64
  waitIndex = 0
  for {
    opts := api.QueryOptions{
      AllowStale: false, 
      RequireConsistent: true, 
      UseCache: false,
      WaitIndex: waitIndex,
    }
    fmt.Println("Will watch consul nodes", waitIndex)
    listNodes, meta, err := ConsulCatalog.Nodes(&opts)
    if err != nil {
      // Prevent backend errors from consuming all resources.
      log.Fatal(err)
      time.Sleep(time.Second * 2)
      continue
    }

    newNodes := make(map[string]string)
    for _, node := range listNodes {
      newNodes[node.Address] = node.ID
    }

    newNodesChan <- newNodes

    waitIndex = meta.LastIndex
  }
  stopMonitorNodesChan <- true
}

func removeLeftNodes(ConsulClient *api.Client, peers map[string]map[string]string, nodesPhysicalIpAddr map[string]string) {
  var ConsulKV *api.KV
  ConsulKV = ConsulClient.KV()
  for physicalIpAddrKey, _ := range peers {
    if _, ok := nodesPhysicalIpAddr[physicalIpAddrKey]; !ok {
      fmt.Println("Release node IP from the pool")
      _, err := ConsulKV.DeleteTree(longKvPrefix + "nodes/" + physicalIpAddrKey, nil)
      if err != nil {
        log.Fatal(err)
      }
    }
  }
}

func incIp(ip net.IP) {
  for j := len(ip) - 1; j >= 0; j-- {
    ip[j]++
    if ip[j] > 0 {
      break
    }
  }
}


func contains(a []string, x string) bool {
  for _, n := range a {
    if x == n {
      return true
    }
  }
  return false
}



