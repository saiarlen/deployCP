package services

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/system"
)

type FirewallService struct {
	cfg    *config.Config
	runner *system.Runner
	audit  *AuditService
}

var firewallColumnSplitRe = regexp.MustCompile(`\s{2,}`)

func NewFirewallService(cfg *config.Config, runner *system.Runner, audit *AuditService) *FirewallService {
	return &FirewallService{cfg: cfg, runner: runner, audit: audit}
}

func (s *FirewallService) ApplyRule(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	if rule == nil {
		return fmt.Errorf("firewall rule is required")
	}
	if err := validateFirewallRule(rule); err != nil {
		return err
	}
	if !rule.Enabled {
		return nil
	}
	switch backend := s.detectBackend(); backend {
	case "ufw":
		return s.applyUFW(ctx, rule, actor, ip)
	case "firewalld":
		return s.applyFirewalld(ctx, rule, actor, ip)
	case "iptables":
		return s.applyIPTables(ctx, rule, actor, ip)
	default:
		return fmt.Errorf("no supported Linux firewall backend found (expected ufw, firewall-cmd, or iptables)")
	}
}

func (s *FirewallService) DeleteRule(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	if rule == nil {
		return fmt.Errorf("firewall rule is required")
	}
	switch backend := s.detectBackend(); backend {
	case "ufw":
		return s.deleteUFW(ctx, rule, actor, ip)
	case "firewalld":
		return s.deleteFirewalld(ctx, rule, actor, ip)
	case "iptables":
		return s.deleteIPTables(ctx, rule, actor, ip)
	default:
		return nil
	}
}

func (s *FirewallService) detectBackend() string {
	if s.cfg != nil && s.cfg.Features.PlatformMode == "dryrun" {
		return ""
	}
	if _, err := exec.LookPath(s.cfg.Paths.UFWBinary); err == nil {
		return "ufw"
	}
	if _, err := exec.LookPath(s.cfg.Paths.FirewallCMDBinary); err == nil {
		return "firewalld"
	}
	if _, err := exec.LookPath(s.cfg.Paths.IPTablesBinary); err == nil {
		return "iptables"
	}
	return ""
}

func (s *FirewallService) HostStatus(ctx context.Context) (string, bool, []models.PanelFirewallRule, error) {
	if s == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return "", false, nil, nil
	}
	switch backend := s.detectBackend(); backend {
	case "ufw":
		return s.ufwStatus(ctx)
	case "firewalld":
		return s.firewalldStatus(ctx)
	case "iptables":
		return s.iptablesStatus(ctx)
	default:
		return "", false, nil, nil
	}
}

func (s *FirewallService) ufwStatus(ctx context.Context) (string, bool, []models.PanelFirewallRule, error) {
	res, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:  s.cfg.Paths.UFWBinary,
		Args:    []string{"status"},
		Timeout: 8 * time.Second,
	})
	combined := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if err != nil && !strings.Contains(strings.ToLower(combined), "inactive") {
		return "ufw", false, nil, err
	}
	active := strings.Contains(strings.ToLower(combined), "status: active")
	if !active {
		return "ufw", false, nil, nil
	}
	rules := make([]models.PanelFirewallRule, 0)
	scanner := bufio.NewScanner(strings.NewReader(res.Stdout))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(strings.ToLower(line), "status:") || strings.HasPrefix(line, "To") || strings.HasPrefix(line, "--") {
			continue
		}
		parts := firewallColumnSplitRe.Split(line, 3)
		if len(parts) < 3 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		action := strings.ToLower(strings.TrimSpace(parts[1]))
		source := strings.TrimSpace(parts[2])
		protocol := "any"
		port := target
		if slash := strings.LastIndex(target, "/"); slash > 0 && slash < len(target)-1 {
			port = strings.TrimSpace(target[:slash])
			protocol = strings.ToLower(strings.TrimSpace(target[slash+1:]))
		}
		rules = append(rules, models.PanelFirewallRule{
			Name:        target,
			Protocol:    protocol,
			Port:        port,
			Source:      source,
			Action:      action,
			Description: "Detected from host UFW",
			Enabled:     true,
		})
	}
	return "ufw", true, rules, scanner.Err()
}

func (s *FirewallService) firewalldStatus(ctx context.Context) (string, bool, []models.PanelFirewallRule, error) {
	stateRes, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:  s.cfg.Paths.FirewallCMDBinary,
		Args:    []string{"--state"},
		Timeout: 8 * time.Second,
	})
	active := strings.TrimSpace(stateRes.Stdout) == "running"
	if err != nil && !active {
		return "firewalld", false, nil, nil
	}

	rules := make([]models.PanelFirewallRule, 0)
	portsRes, portsErr := s.runner.Run(ctx, system.CommandRequest{
		Binary:  s.cfg.Paths.FirewallCMDBinary,
		Args:    []string{"--list-ports"},
		Timeout: 8 * time.Second,
	})
	if portsErr == nil {
		for _, item := range strings.Fields(strings.TrimSpace(portsRes.Stdout)) {
			port := item
			protocol := "any"
			if slash := strings.LastIndex(item, "/"); slash > 0 && slash < len(item)-1 {
				port = item[:slash]
				protocol = item[slash+1:]
			}
			rules = append(rules, models.PanelFirewallRule{
				Name:        item,
				Protocol:    strings.ToLower(strings.TrimSpace(protocol)),
				Port:        strings.TrimSpace(port),
				Source:      "any",
				Action:      "allow",
				Description: "Detected from host firewalld ports",
				Enabled:     true,
			})
		}
	}

	servicesRes, svcErr := s.runner.Run(ctx, system.CommandRequest{
		Binary:  s.cfg.Paths.FirewallCMDBinary,
		Args:    []string{"--list-services"},
		Timeout: 8 * time.Second,
	})
	if svcErr == nil {
		for _, item := range strings.Fields(strings.TrimSpace(servicesRes.Stdout)) {
			rules = append(rules, models.PanelFirewallRule{
				Name:        item,
				Protocol:    "service",
				Port:        item,
				Source:      "any",
				Action:      "allow",
				Description: "Detected from host firewalld services",
				Enabled:     true,
			})
		}
	}
	return "firewalld", active, rules, nil
}

func (s *FirewallService) iptablesStatus(ctx context.Context) (string, bool, []models.PanelFirewallRule, error) {
	res, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:  s.cfg.Paths.IPTablesBinary,
		Args:    []string{"-S", "INPUT"},
		Timeout: 8 * time.Second,
	})
	if err != nil {
		return "iptables", false, nil, err
	}
	rules := make([]models.PanelFirewallRule, 0)
	scanner := bufio.NewScanner(strings.NewReader(res.Stdout))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "-A INPUT") {
			continue
		}
		fields := strings.Fields(line)
		rule := models.PanelFirewallRule{
			Name:        line,
			Protocol:    "any",
			Port:        "any",
			Source:      "0.0.0.0/0",
			Action:      "allow",
			Description: "Detected from host iptables INPUT chain",
			Enabled:     true,
		}
		for i := 0; i < len(fields); i++ {
			switch fields[i] {
			case "-p":
				if i+1 < len(fields) {
					rule.Protocol = strings.ToLower(fields[i+1])
				}
			case "-s":
				if i+1 < len(fields) {
					rule.Source = fields[i+1]
				}
			case "--dport":
				if i+1 < len(fields) {
					rule.Port = fields[i+1]
				}
			case "-j":
				if i+1 < len(fields) {
					target := strings.ToUpper(fields[i+1])
					switch target {
					case "DROP", "REJECT":
						rule.Action = "deny"
					default:
						rule.Action = "allow"
					}
				}
			}
		}
		rules = append(rules, rule)
	}
	return "iptables", true, rules, scanner.Err()
}

func (s *FirewallService) applyUFW(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	args := []string{"--force", rule.Action}
	if source := normalizedRuleSource(rule.Source); source != "" {
		args = append(args, "from", source)
	}
	args = append(args, "to", "any")
	if port := strings.TrimSpace(rule.Port); port != "" && port != "any" {
		args = append(args, "port", port)
	}
	if proto := normalizedRuleProtocol(rule.Protocol); proto != "" && proto != "any" && proto != "icmp" {
		args = append(args, "proto", proto)
	}
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.UFWBinary,
		Args:        args,
		Timeout:     30 * time.Second,
		AuditAction: "firewall.ufw.apply",
		ActorUserID: actor,
		IP:          ip,
	})
	if err == nil {
		s.audit.Record(actor, "firewall.apply", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "ufw", "name": rule.Name})
	}
	return err
}

func (s *FirewallService) deleteUFW(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	args := []string{"--force", "delete", rule.Action}
	if source := normalizedRuleSource(rule.Source); source != "" {
		args = append(args, "from", source)
	}
	args = append(args, "to", "any")
	if port := strings.TrimSpace(rule.Port); port != "" && port != "any" {
		args = append(args, "port", port)
	}
	if proto := normalizedRuleProtocol(rule.Protocol); proto != "" && proto != "any" && proto != "icmp" {
		args = append(args, "proto", proto)
	}
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.UFWBinary,
		Args:        args,
		Timeout:     30 * time.Second,
		AuditAction: "firewall.ufw.delete",
		ActorUserID: actor,
		IP:          ip,
	})
	if err == nil {
		s.audit.Record(actor, "firewall.delete", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "ufw", "name": rule.Name})
	}
	return nil
}

func (s *FirewallService) applyFirewalld(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	richRule := s.firewalldRichRule(rule)
	if richRule == "" {
		return fmt.Errorf("unable to render firewalld rule")
	}
	for _, args := range [][]string{{"--permanent", "--add-rich-rule", richRule}, {"--reload"}} {
		if _, err := s.runner.Run(ctx, system.CommandRequest{
			Binary:      s.cfg.Paths.FirewallCMDBinary,
			Args:        args,
			Timeout:     30 * time.Second,
			AuditAction: "firewall.firewalld.apply",
			ActorUserID: actor,
			IP:          ip,
		}); err != nil {
			return err
		}
	}
	s.audit.Record(actor, "firewall.apply", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "firewalld", "name": rule.Name})
	return nil
}

func (s *FirewallService) deleteFirewalld(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	richRule := s.firewalldRichRule(rule)
	if richRule == "" {
		return nil
	}
	for _, args := range [][]string{{"--permanent", "--remove-rich-rule", richRule}, {"--reload"}} {
		if _, err := s.runner.Run(ctx, system.CommandRequest{
			Binary:      s.cfg.Paths.FirewallCMDBinary,
			Args:        args,
			Timeout:     30 * time.Second,
			AuditAction: "firewall.firewalld.delete",
			ActorUserID: actor,
			IP:          ip,
		}); err != nil {
			return nil
		}
	}
	s.audit.Record(actor, "firewall.delete", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "firewalld", "name": rule.Name})
	return nil
}

func (s *FirewallService) firewalldRichRule(rule *models.PanelFirewallRule) string {
	source := normalizedRuleSource(rule.Source)
	action := "accept"
	if strings.EqualFold(rule.Action, "deny") {
		action = "drop"
	}
	parts := []string{"rule"}
	if source != "" && source != "0.0.0.0/0" && source != "::/0" {
		parts = append(parts, fmt.Sprintf("source address=\"%s\"", source))
	}
	if port := strings.TrimSpace(rule.Port); port != "" && port != "any" {
		proto := normalizedRuleProtocol(rule.Protocol)
		if proto == "any" || proto == "icmp" || proto == "" {
			proto = "tcp"
		}
		parts = append(parts, fmt.Sprintf("port port=\"%s\" protocol=\"%s\"", port, proto))
	}
	parts = append(parts, action)
	return strings.Join(parts, " ")
}

func (s *FirewallService) applyIPTables(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	args := s.iptablesArgs("-A", rule)
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.IPTablesBinary,
		Args:        args,
		Timeout:     20 * time.Second,
		AuditAction: "firewall.iptables.apply",
		ActorUserID: actor,
		IP:          ip,
	})
	if err == nil {
		s.audit.Record(actor, "firewall.apply", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "iptables", "name": rule.Name})
	}
	return err
}

func (s *FirewallService) deleteIPTables(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
	_, _ = s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.IPTablesBinary,
		Args:        s.iptablesArgs("-D", rule),
		Timeout:     20 * time.Second,
		AuditAction: "firewall.iptables.delete",
		ActorUserID: actor,
		IP:          ip,
	})
	s.audit.Record(actor, "firewall.delete", "firewall_rule", fmt.Sprintf("%d", rule.ID), ip, map[string]any{"backend": "iptables", "name": rule.Name})
	return nil
}

func (s *FirewallService) iptablesArgs(op string, rule *models.PanelFirewallRule) []string {
	action := "ACCEPT"
	if strings.EqualFold(rule.Action, "deny") {
		action = "DROP"
	}
	args := []string{op, "INPUT"}
	if proto := normalizedRuleProtocol(rule.Protocol); proto != "" && proto != "any" {
		if proto == "icmp" {
			args = append(args, "-p", "icmp")
		} else {
			args = append(args, "-p", proto)
		}
	}
	if source := normalizedRuleSource(rule.Source); source != "" {
		args = append(args, "-s", source)
	}
	if port := strings.TrimSpace(rule.Port); port != "" && port != "any" {
		args = append(args, "--dport", port)
	}
	args = append(args, "-m", "comment", "--comment", "deploycp:"+rule.Name, "-j", action)
	return args
}

func validateFirewallRule(rule *models.PanelFirewallRule) error {
	if rule == nil {
		return fmt.Errorf("rule is required")
	}
	switch normalizedRuleProtocol(rule.Protocol) {
	case "tcp", "udp", "icmp", "any":
	default:
		return fmt.Errorf("unsupported firewall protocol")
	}
	if port := strings.TrimSpace(rule.Port); port != "" && port != "any" {
		if strings.Contains(port, ",") {
			return fmt.Errorf("comma-separated port lists are not supported")
		}
	}
	source := normalizedRuleSource(rule.Source)
	if source != "" && source != "0.0.0.0/0" && source != "::/0" {
		if _, _, err := net.ParseCIDR(source); err != nil && net.ParseIP(source) == nil {
			return fmt.Errorf("invalid firewall source")
		}
	}
	return nil
}

func normalizedRuleProtocol(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "tcp"
	}
	return v
}

func normalizedRuleSource(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return "0.0.0.0/0"
	}
	return v
}
