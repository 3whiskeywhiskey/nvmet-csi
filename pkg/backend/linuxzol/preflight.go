package linuxzol

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

type PreflightCheck struct {
	Name   string
	OK     bool
	Detail string
}

func (h *CommandHost) Preflight(ctx context.Context, port int) []PreflightCheck {
	checks := []PreflightCheck{
		h.checkCommand(ctx, "zfs pool", Command{
			Name: "zfs",
			Args: []string{"list", "-H", "-o", "name", h.cfg.Pool},
		}, func(out string) (bool, string) {
			got := strings.Fields(strings.TrimSpace(out))
			if len(got) == 0 {
				return false, fmt.Sprintf("pool %q was not listed", h.cfg.Pool)
			}
			if got[0] != h.cfg.Pool {
				return false, fmt.Sprintf("listed %q, expected %q", got[0], h.cfg.Pool)
			}
			return true, fmt.Sprintf("pool %q exists", h.cfg.Pool)
		}),
		h.checkCommand(ctx, "nvmet configfs", Command{Name: "test", Args: []string{"-d", h.cfg.ConfigFSRoot}}, nil),
		h.checkCommand(ctx, "zvol device root", Command{Name: "test", Args: []string{"-d", h.cfg.DeviceRoot}}, nil),
		h.checkCommand(ctx, "target address", Command{Name: "ip", Args: []string{"-j", "addr", "show"}}, targetInterfaceValid(h.cfg.Address, h.cfg.MinMTU)),
	}

	checks = append(checks, h.preflightPortChecks(ctx, port)...)
	return checks
}

func (h *CommandHost) checkCommand(ctx context.Context, name string, cmd Command, interpret func(string) (bool, string)) PreflightCheck {
	out, err := h.run(ctx, cmd)
	if err != nil {
		return PreflightCheck{Name: name, OK: false, Detail: err.Error()}
	}
	if interpret != nil {
		ok, detail := interpret(out)
		return PreflightCheck{Name: name, OK: ok, Detail: detail}
	}
	return PreflightCheck{Name: name, OK: true, Detail: cmd.String()}
}

func (h *CommandHost) preflightPortChecks(ctx context.Context, port int) []PreflightCheck {
	portPath := h.cfg.portPath(port)
	if _, err := h.run(ctx, Command{Name: "test", Args: []string{"-d", portPath}}); err != nil {
		return []PreflightCheck{{
			Name:   "nvmet port",
			OK:     true,
			Detail: fmt.Sprintf("%s is absent; create/repair will create it", portPath),
		}}
	}

	return []PreflightCheck{
		h.checkPortAttribute(ctx, port, "addr_trtype", h.cfg.portTransport()),
		h.checkPortAttribute(ctx, port, "addr_adrfam", h.cfg.portAddressFamily()),
		h.checkPortAttribute(ctx, port, "addr_traddr", h.cfg.Address),
		h.checkPortAttribute(ctx, port, "addr_trsvcid", h.cfg.portServiceID(port)),
	}
}

func (h *CommandHost) checkPortAttribute(ctx context.Context, port int, name string, want string) PreflightCheck {
	path := h.cfg.portPath(port) + "/" + name
	out, err := h.run(ctx, Command{Name: "cat", Args: []string{path}})
	if err != nil {
		return PreflightCheck{Name: "nvmet port " + name, OK: false, Detail: err.Error()}
	}
	got := strings.TrimSpace(out)
	if got == "" {
		return PreflightCheck{
			Name:   "nvmet port " + name,
			OK:     true,
			Detail: fmt.Sprintf("%s is empty; create/repair will set %q", path, want),
		}
	}
	if got != want {
		return PreflightCheck{
			Name:   "nvmet port " + name,
			OK:     false,
			Detail: fmt.Sprintf("%s is %q, expected %q", path, got, want),
		}
	}
	return PreflightCheck{
		Name:   "nvmet port " + name,
		OK:     true,
		Detail: fmt.Sprintf("%s already matches %q", path, want),
	}
}

func PreflightChecksOK(checks []PreflightCheck) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func targetInterfaceValid(address string, minMTU int) func(string) (bool, string) {
	return func(out string) (bool, string) {
		want := net.ParseIP(strings.TrimSpace(address))
		if want == nil {
			return false, fmt.Sprintf("target address %q is not a valid IP address", address)
		}

		var links []struct {
			IfName   string `json:"ifname"`
			MTU      int    `json:"mtu"`
			AddrInfo []struct {
				Local string `json:"local"`
			} `json:"addr_info"`
		}
		if err := json.Unmarshal([]byte(out), &links); err != nil {
			return false, fmt.Sprintf("failed to parse ip address inventory: %v", err)
		}

		for _, link := range links {
			for _, addr := range link.AddrInfo {
				if got := net.ParseIP(addr.Local); got != nil && got.Equal(want) {
					if minMTU > 0 && link.MTU < minMTU {
						return false, fmt.Sprintf("target address %s exists on interface %s, but MTU %d is below required %d", address, link.IfName, link.MTU, minMTU)
					}
					if minMTU > 0 {
						return true, fmt.Sprintf("target address %s exists on interface %s with MTU %d", address, link.IfName, link.MTU)
					}
					return true, fmt.Sprintf("target address %s exists on interface %s", address, link.IfName)
				}
			}
		}
		return false, fmt.Sprintf("target address %s is not assigned on this host", address)
	}
}
