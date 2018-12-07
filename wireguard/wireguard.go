package wireguard

import (
  "bytes"
  "fmt"
  "io"
  "strings"
  "strconv"
  "os"
  "os/exec"
  "text/template"
  "github.com/geniousphp/autowire/ifconfig"
)

type Interface struct {
  Name string
  Address string
  ListenPort int
  PrivateKey string
}

type Peer struct {
  PublicKey  string
  Ip         string
  AllowedIPs string
  Endpoint   string
  Port       int
}

type WGConfig struct {
  Interface Interface
  Peers     map[string]Peer
}

func wg(stdin io.Reader, arg ...string) ([]byte, error) {
  path, err := exec.LookPath("wg")
  if err != nil {
    return nil, fmt.Errorf("the wireguard (wg) command is not available in your PATH")
  }

  cmd := exec.Command(path, arg...)

  cmd.Stdin = stdin
  var buf bytes.Buffer
  cmd.Stderr = &buf
  output, err := cmd.Output()

  if err != nil {
    return nil, fmt.Errorf("%s - %s", err.Error(), buf.String())
  }
  return output, nil

}

func Genkey() ([]byte, error) {
  result, err := wg(nil, "genkey")
  if err != nil {
    return nil, fmt.Errorf("error generating the private key for wireguard: %s", err.Error())
  }
  return result, nil
}

func ExtractPubKey(privateKey []byte) ([]byte, error) {
  stdin := bytes.NewReader(privateKey)
  result, err := wg(stdin, "pubkey")
  if err != nil {
    return nil, fmt.Errorf("error extracting the public key: %s", err.Error())
  }
  return result, nil
}


func ConfigureInterface(wgConfig WGConfig) (error) {
  configFile, err := os.Create("/etc/wireguard/" + wgConfig.Interface.Name + ".conf")
  if err != nil {
    return err
  }

  t := template.Must(template.New("config").Parse(wgConfigTemplate))

  err = t.Execute(configFile, wgConfig.Interface)
  if err != nil {
    return err
  }

  configFile.Chmod(0600)
  return nil
}

func IsWgInterfaceWellConfigured(wgConfig WGConfig) (bool) {
  // Check consistency with ip addr show dev wg0 (IP Address)
  actualIpAddr, _ := ifconfig.GetIpOfIf(wgConfig.Interface.Name)
  if(actualIpAddr != wgConfig.Interface.Address){
    return false
  }

  // Check consistency with wg show wg0 (Port and Private Key)
  result, _ := wg(nil, "show", wgConfig.Interface.Name, "dump")
  currentWgConfig := strings.Split(string(result[:]), "\t")

  if(currentWgConfig[0] != wgConfig.Interface.PrivateKey){
    return false
  }

  currentWgPort, _ := strconv.Atoi(currentWgConfig[2])
  if(currentWgPort != wgConfig.Interface.ListenPort){
    return false
  }

  return true
}

func ConfigurePeer(wgInterfaceName string, peer map[string]string) ([]byte, error) {
  result, err := wg(nil, "set", wgInterfaceName, "peer", peer["pubKey"], "endpoint", peer["endpoint"] + ":" + peer["port"], "allowed-ips", peer["allowedips"])
  if err != nil {
    return nil, fmt.Errorf("error configuring wg peer: %s", err.Error())
  }
  return result, nil
}

func RemovePeer(wgInterfaceName string, peer map[string]string) ([]byte, error) {
  result, err := wg(nil, "set", wgInterfaceName, "peer", peer["pubKey"], "remove")
  if err != nil {
    return nil, fmt.Errorf("error removing wg peer: %s", err.Error())
  }
  return result, nil
}

