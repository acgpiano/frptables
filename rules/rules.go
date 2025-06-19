//MIT License
//
//Copyright (c) 2021 zngw
//
//Permission is hereby granted, free of charge, to any person obtaining a copy
//of this software and associated documentation files (the "Software"), to deal
//in the Software without restriction, including without limitation the rights
//to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//copies of the Software, and to permit persons to whom the Software is
//furnished to do so, subject to the following conditions:
//
//The above copyright notice and this permission notice shall be included in all
//copies or substantial portions of the Software.
//
//THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//SOFTWARE.

package rules

import (
	"encoding/json"
	"fmt"
	"github.com/zngw/frptables/config"
	"github.com/zngw/frptables/util"
	"github.com/zngw/golib/log"
	"io"
	"net/http"
)

// 拦截IP-端口，因为验证ip有时间差，攻击频率太高会导致防火墙重复添加。
var RefuseMap = make(map[string]bool)

func Init() {
	rateInit()
}

// ip规则判断
// ip-访问者IP，name-连接名字，port-连接端口
func rules(ip, name string, port int) {
	// 检查白名单
	if checkAllow(ip, port) {
		log.Trace("link", name, ip, port, "is allow")
		return
	}

	result, desc, p, count := CheckRules(ip, port)
	if result {
		refuse(ip, name, p)
		log.Trace("add", fmt.Sprintf("refuse: [%s]%s:%d ->%d, %s", name, ip, port, count, desc))
		return
	} else {
		log.Trace("link", fmt.Sprintf("allow: [%s]%s:%d ->%d, %s", name, ip, port, count, desc))
		return
	}
}

// 检查白名单
func checkAllow(ip string, port int) bool {
	for _, v := range config.Cfg.AllowIp {
		if v == ip {
			return true
		}
	}

	for _, v := range config.Cfg.AllowPort {
		if v == port {
			return true
		}
	}

	return false
}

// 检测访问规则
// ip-访问者IP， port-转发端口
// 返回： refuse-是否加入规则拒绝访问, desc-描述， p-拒绝访问端口, count-规则间隔内访问次数
func CheckRules(ip string, port int) (refuse bool, desc string, p, count int) {
	info := getIpHistory(ip)
	if !info.HasInfo {
		// 通过 https://ip.zengwu.com.cn?ip= 接口获取ip归属地信息
		// 详细文档可查看： https://zengwu.com.cn/archives/ip
		// 这里也可以切换成自己的ip查询
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "https://ip.zengwu.com.cn?ip="+ip, nil)
		resp, err := client.Do(req)
		if err != nil {
			// 读取网页数据错误
			return
		}

		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 {
			// 读取网页数据错误
			return
		}

		var jsonInfo struct {
			Result   int32  `json:"result,omitempty"`   // 状态
			Country  string `json:"country,omitempty"`  // 国家
			Province string `json:"province,omitempty"` // 省
			City     string `json:"city,omitempty"`     // 城市
			Isp      string `json:"isp,omitempty"`      // 运营商
			Query    string `json:"query,omitempty"`    // 查询IP
			Time     int64  `json:"time,omitempty"`     // 查询时间
		}

		err = json.Unmarshal(body, &jsonInfo)
		if err != nil {
			return
		}

		info.HasInfo = true

		if err != nil || jsonInfo.Result != 0 {
			// 地址获取不成功，跳过
			refuse = false
			p = -1
			return
		}

		info.Country = jsonInfo.Country
		info.Region = jsonInfo.Province
		info.City = jsonInfo.City
		// 获取IP信息结束
	}
	info.Add()

	for _, v := range config.Cfg.Rules {
		if v.Port != -1 && v.Port != port {
			// 端口不匹配
			continue
		}

		if v.Country != "" && v.Country != info.Country {
			// 国家不匹配
			continue
		}

		if v.RegionName != "" && v.RegionName != info.Region {
			// 省不匹配
			continue
		}

		if v.City != "" && v.City != info.City {
			// 城市不匹配
			continue
		}

		p = v.Port
		desc = fmt.Sprintf("%s,%s,%s", info.Country, info.Region, info.City)

		// 跳过
		if v.Count < 0 {
			refuse = false
			return
		}

		// 拒绝访问
		if v.Count == 0 {
			refuse = true
			return
		}

		count = info.Count(v.Time)
		if count <= v.Count {
			refuse = false
			return
		}

		refuse = true
		delIpHistory(ip, port)
		return
	}

	return
}

// 添加防火墙，拒绝访问
func refuse(ip, name string, port int) {
	// 检测IP是否有添加记录
	if _, ok := RefuseMap[ip]; ok {
		// ip添加
		return
	}

	// 检测IP:Port是否有添加记录
	if port != -1 {
		key := fmt.Sprintf("%s:%d", ip, port)
		if _, ok := RefuseMap[key]; ok {
			return
		}

		RefuseMap[key] = true
	} else {
		RefuseMap[ip] = true
	}

	cmd := ""
	switch config.Cfg.TablesType {
	case "iptables":
		if port == -1 {
			cmd = fmt.Sprintf("iptables -I INPUT -s %s -j DROP", ip)
		} else {
			cmd = fmt.Sprintf("iptables -I INPUT -s %s -ptcp --dport %d -j DROP", ip, port)
		}
	case "firewall":
		if port == -1 {
			cmd = fmt.Sprintf("firewall-cmd --permanent --add-rich-rule=\"rule family=\"ipv4\" source address=\"%s\" reject\"", ip)
		} else {
			cmd = fmt.Sprintf("firewall-cmd --permanent --add-rich-rule=\"rule family=\"ipv4\" source address=\"%s\" port protocol=\"tcp\" port=\"%d\" reject\"", ip, port)
		}
		cmd += "&& firewall-cmd --reload"
	case "md":
		if port == -1 {
			cmd = fmt.Sprintf("netsh advfirewall firewall add rule name=%s-%s dir=in action=block protocol=TCP remoteip=%s", name, ip, ip)
		} else {
			cmd = fmt.Sprintf("netsh advfirewall firewall add rule name=%s-%s-%d dir=in action=block protocol=TCP remoteip=%s localport=%d", name, ip, port, ip, port)
		}
	}

	result := util.Command(cmd)
	if result != "" {
		log.Trace("sys", result)
	}
}
