package v2ray

import (
	"github.com/json-iterator/go"
	"github.com/v2rayA/v2rayA/common/netTools/netstat"
	"github.com/v2rayA/v2rayA/common/netTools/ports"
	"github.com/v2rayA/v2rayA/common/ntp"
	"github.com/v2rayA/v2rayA/common/resolv"
	"github.com/v2rayA/v2rayA/core/specialMode"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/core/vmessInfo"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/global"
	"github.com/v2rayA/v2rayA/plugin"
	"log"
	"os"
	"path"
	"strings"
	"time"
)

var v2RayPID *os.Process
var tag2WhichIndex map[string]int
var apiPort int

func SetCoreProcess(p *os.Process) {
	configure.SetRunning(p != nil)
	v2RayPID = p
}
func CoreProcess() *os.Process {
	return v2RayPID
}
func IsV2RayRunning() bool {
	return CoreProcess() != nil
}
func ApiPort() int {
	if !IsV2RayRunning() {
		return 0
	}
	return apiPort
}

func RestartV2rayService(saveStatus bool) (err error) {
	if ok, t, err := ntp.IsDatetimeSynced(); err == nil && !ok {
		return newError("Please sync datetime first. Your datetime is ", time.Now().Local().Format(ntp.DisplayFormat), ", and the correct datetime is ", t.Local().Format(ntp.DisplayFormat))
	}
	setting := configure.GetSettingNotNil()
	if (setting.Transparent == configure.TransparentGfwlist || setting.RulePortMode == configure.GfwlistMode) && !asset.IsGFWListExists() {
		return newError("cannot find GFWList files. update GFWList and try again")
	}
	//关闭transparentProxy，防止v2ray在启动DOH时需要解析域名
	if err = killV2ray(saveStatus); err != nil {
		return
	}
	v2rayBinPath, err := where.GetV2rayBinPath()
	if err != nil {
		return
	}
	dir := path.Dir(v2rayBinPath)
	var params = []string{
		v2rayBinPath,
		"--config=" + asset.GetV2rayConfigPath(),
	}
	if confdir := asset.GetV2rayConfigDirPath(); confdir != "" {
		params = append(params, "--confdir="+confdir)
	}
	log.Println(strings.Join(params, " "))
	assetDir := asset.GetV2rayLocationAsset()
	process, err := os.StartProcess(v2rayBinPath, params, &os.ProcAttr{
		Dir: dir, //防止找不到v2ctl
		Env: append(os.Environ(),
			"V2RAY_LOCATION_ASSET="+assetDir,
			"XRAY_LOCATION_ASSET="+assetDir,
		),
		Files: []*os.File{
			os.Stderr,
			os.Stdout,
		},
	})
	if err != nil {
		return newError().Base(err)
	}
	if saveStatus {
		SetCoreProcess(process)
	}
	defer func() {
		if err != nil && CoreProcess() != nil {
			_ = killV2ray(true)
		}
	}()
	//如果inbounds中开放端口，检测端口是否已就绪
	tmplJson := Template{}
	var b []byte
	b, err = asset.GetConfigBytes()
	if err != nil {
		return
	}
	err = jsoniter.Unmarshal(b, &tmplJson)
	if err != nil {
		return
	}
	bPortOpen := false
	var port int
	for _, v := range tmplJson.Inbounds {
		if v.Port != 0 {
			if v.Settings != nil && v.Settings.Network != "" && !strings.Contains(v.Settings.Network, "tcp") {
				continue
			}
			bPortOpen = true
			port = v.Port
			break
		}
	}
	//defer func() {
	//	log.Println(port)
	//	log.Println("\n" + netstat.Print([]string{"tcp", "tcp6"}))
	//	log.Println("\n" + testprint())
	//}()
	startTime := time.Now()
	for {
		if bPortOpen {
			var is bool
			is, err = netstat.IsProcessListenPort(path.Base(v2rayBinPath), port)
			if err != nil {
				return
			}
			if is {
				break
			}
		} else {
			if IsV2RayRunning() {
				time.Sleep(1000 * time.Millisecond) //距离v2ray进程启动到端口绑定可能需要一些时间
				break
			}
		}

		if time.Since(startTime) > 15*time.Second {
			return newError("v2ray-core does not start normally, check the log for more information")
		}
		time.Sleep(1000 * time.Millisecond)
	}
	return
}

func findAvailablePluginPorts(vms []vmessInfo.VmessInfo) (pluginPortMap map[int]int, err error) {
	nsmap, err := netstat.ToPortMap([]string{"tcp", "tcp6"})
	if err != nil {
		return nil, err
	}
	pluginPortMap = make(map[int]int)
	checkingPort := global.GetEnvironmentConfig().PluginListenPort - 1
	for i, v := range vms {
		if !plugin.HasProperPlugin(v) {
			continue
		}
		//find a port that not be occupied
		checkingPort++
		for {
			if !ports.IsOccupiedTCPPort(nsmap, checkingPort) {
				// this port is available
				break
			}
			checkingPort++
		}
		pluginPortMap[i] = checkingPort
	}
	return pluginPortMap, nil
}

func UpdateV2RayConfig() (err error) {
	resolv.CheckResolvConf()
	CheckAndStopTransparentProxy()
	defer func() {
		if err == nil {
			if e := CheckAndSetupTransparentProxy(true); e != nil {
				err = newError(e).Base(err)
				if IsV2RayRunning() {
					if e = StopV2rayService(true); e != nil {
						err = newError(e).Base(err)
					}
				}
			}
		}
	}()
	plugin.GlobalPlugins.CloseAll()
	specialMode.StopDNSSupervisor()
	//read the database and convert to the v2ray-core template
	var (
		tmpl Template
		sr   *configure.ServerRaw
	)
	css := configure.GetConnectedServers()
	if css.Len() == 0 { //no connected server. stop v2ray-core.
		return StopV2rayService(true)
	}
	outboundInfos := make([]OutboundInfo, 0, css.Len())
	vms := make([]vmessInfo.VmessInfo, 0, css.Len())
	for _, cs := range css.Get() {
		sr, err = cs.LocateServerRaw()
		if err != nil {
			return
		}
		outboundInfos = append(outboundInfos, OutboundInfo{
			Info:         sr.VmessInfo,
			OutboundName: cs.Outbound,
		})
		vms = append(vms, sr.VmessInfo)
	}
	var pluginPorts map[int]int
	if pluginPorts, err = findAvailablePluginPorts(vms); err != nil {
		return err
	}
	for i := range outboundInfos {
		if port, ok := pluginPorts[i]; ok {
			outboundInfos[i].PluginPort = port
		}
	}
	var outboundTags []string
	tmpl, outboundTags, err = NewTemplate(outboundInfos)
	if err != nil {
		return
	}
	// NOTICE: tag2WhichIndex is reliable because once connected servers are changed when v2ray is running,
	// the func UpdateV2RayConfig should be invoked and tag2WhichIndex will be regenerated.
	tag2WhichIndex = make(map[string]int)
	for i, tag := range outboundTags {
		tag2WhichIndex[tag] = i
	}
	err = WriteV2rayConfig(tmpl.ToConfigBytes())
	if err != nil {
		return
	}

	if css.Len() == 0 && !IsV2RayRunning() {
		//no need to restart if no connected servers
		return
	}
	if err = tmpl.CheckInboundPortsOccupied(); err != nil {
		return newError(err)
	}
	err = RestartV2rayService(true)
	if err != nil {
		return
	}

	// try launching plugin clients
	for i, v := range vms {
		var (
			port int
			ok   bool
		)
		if port, ok = pluginPorts[i]; !ok {
			continue
		}
		var plu plugin.Plugin
		plu, err = plugin.NewPluginAndServe(port, v)
		if err != nil {
			return
		}
		if plu != nil {
			plugin.GlobalPlugins.Add(css.Get()[i].Outbound, plu)
		}
	}

	specialMode.CheckAndSetupDNSSupervisor()
	return
}

func killV2ray(saveStatus bool) (err error) {
	if CoreProcess() != nil {
		err = CoreProcess().Kill()
		if err != nil {
			return
		}
		if saveStatus {
			SetCoreProcess(nil)
		}
	}
	return
}

func StopV2rayService(saveStatus bool) (err error) {
	defer CheckAndStopTransparentProxy()
	defer plugin.GlobalPlugins.CloseAll()
	defer specialMode.StopDNSSupervisor()
	defer func() {
		if IsV2RayRunning() {
			msg := "failed to stop v2ray"
			if err != nil && len(strings.TrimSpace(err.Error())) > 0 {
				msg += ": " + err.Error()
			}
			err = newError(msg)
		}
	}()
	return killV2ray(saveStatus)
}
