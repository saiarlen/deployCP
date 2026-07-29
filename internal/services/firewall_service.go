package services

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	if s.ruleDeniesActiveSSH(ctx, rule, ip) {
		return fmt.Errorf("refusing to add firewall rule that blocks the active SSH port")
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
	if s.ruleAllowsActiveSSH(ctx, rule, ip) {
		return fmt.Errorf("refusing to delete firewall rule that allows the active SSH port")
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

func (s *FirewallService) ReplaceRule(ctx context.Context, existing, next *models.PanelFirewallRule, actor *uint, ip string) error {
	if existing == nil {
		return s.ApplyRule(ctx, next, actor, ip)
	}
	if next == nil {
		return fmt.Errorf("firewall rule is required")
	}
	if err := validateFirewallRule(next); err != nil {
		return err
	}
	if s.ruleAllowsActiveSSH(ctx, existing, ip) {
		if !sameFirewallHostEffect(existing, next) {
			if !s.ruleAllowsActiveSSHFromIP(ctx, next, ip) {
				return fmt.Errorf("refusing to modify firewall rule that allows the active SSH port")
			}
			if err := s.ApplyRule(ctx, next, actor, ip); err != nil {
				return err
			}
			return s.deleteRuleUnchecked(ctx, existing, actor, ip)
		}
		return nil
	}
	if s.ruleDeniesActiveSSH(ctx, next, ip) {
		return fmt.Errorf("refusing to add firewall rule that blocks the active SSH port")
	}
	if err := s.DeleteRule(ctx, existing, actor, ip); err != nil {
		return err
	}
	return s.ApplyRule(ctx, next, actor, ip)
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

func (s *FirewallService) ruleAllowsActiveSSH(ctx context.Context, rule *models.PanelFirewallRule, ip string) bool {
	if !s.sshProtectionEnabled() {
		return false
	}
	return ruleUsesActiveSSHPort(rule, s.activeSSHPorts(ctx)) &&
		sourceAllowsIP(rule.Source, ip) &&
		strings.EqualFold(rule.Action, "allow")
}

func (s *FirewallService) ruleDeniesActiveSSH(ctx context.Context, rule *models.PanelFirewallRule, ip string) bool {
	if !s.sshProtectionEnabled() {
		return false
	}
	return ruleUsesActiveSSHPort(rule, s.activeSSHPorts(ctx)) &&
		sourceAllowsIP(rule.Source, ip) &&
		strings.EqualFold(rule.Action, "deny")
}

func (s *FirewallService) ruleAllowsActiveSSHFromIP(ctx context.Context, rule *models.PanelFirewallRule, ip string) bool {
	if rule == nil || !s.sshProtectionEnabled() || !strings.EqualFold(strings.TrimSpace(rule.Action), "allow") {
		return false
	}
	return ruleUsesActiveSSHPort(rule, s.activeSSHPorts(ctx)) && sourceAllowsIP(rule.Source, ip)
}

func (s *FirewallService) sshProtectionEnabled() bool {
	return s != nil && (s.cfg == nil || s.cfg.Features.PlatformMode != "dryrun")
}

func ruleCoversActiveSSHPort(rule *models.PanelFirewallRule, sshPorts map[string]struct{}) bool {
	if !ruleUsesActiveSSHPort(rule, sshPorts) {
		return false
	}
	source := normalizedRuleSource(rule.Source)
	return source == "0.0.0.0/0" || source == "::/0"
}

func ruleUsesActiveSSHPort(rule *models.PanelFirewallRule, sshPorts map[string]struct{}) bool {
	if rule == nil || !rule.Enabled {
		return false
	}
	protocol := normalizedRuleProtocol(rule.Protocol)
	if protocol != "tcp" && protocol != "any" {
		return false
	}
	for port := range sshPorts {
		if portSpecIncludes(strings.TrimSpace(rule.Port), port) {
			return true
		}
	}
	return false
}

func sourceAllowsIP(source, requester string) bool {
	source = normalizedRuleSource(source)
	if source == "0.0.0.0/0" || source == "::/0" {
		return true
	}
	ip := parseRequestIP(requester)
	if ip == nil {
		return false
	}
	if _, cidr, err := net.ParseCIDR(source); err == nil {
		return cidr.Contains(ip)
	}
	sourceIP := net.ParseIP(source)
	return sourceIP != nil && sourceIP.Equal(ip)
}

func parseRequestIP(value string) net.IP {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.Contains(value, ",") {
		value = strings.TrimSpace(strings.Split(value, ",")[0])
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return net.ParseIP(value)
}

func sameFirewallHostEffect(a, b *models.PanelFirewallRule) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Enabled == b.Enabled &&
		normalizedRuleProtocol(a.Protocol) == normalizedRuleProtocol(b.Protocol) &&
		normalizedRuleSource(a.Source) == normalizedRuleSource(b.Source) &&
		normalizedPortSpec(a.Port) == normalizedPortSpec(b.Port) &&
		strings.EqualFold(strings.TrimSpace(a.Action), strings.TrimSpace(b.Action))
}

func (s *FirewallService) activeSSHPorts(ctx context.Context) map[string]struct{} {
	ports := map[string]struct{}{}
	if binary := sshdBinaryPath(); binary != "" {
		_ = ensureRuntimeDir("/run/sshd", 0o755)
		if s.runner != nil {
			res, err := s.runner.Run(ctx, system.CommandRequest{
				Binary:  binary,
				Args:    []string{"-T"},
				Timeout: 8 * time.Second,
			})
			if err == nil {
				addSSHDPorts(ports, res.Stdout)
			}
		}
	}
	if len(ports) == 0 {
		addSSHConfigPorts(ports)
	}
	if len(ports) == 0 {
		ports["22"] = struct{}{}
	}
	return ports
}

func (s *FirewallService) deleteRuleUnchecked(ctx context.Context, rule *models.PanelFirewallRule, actor *uint, ip string) error {
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
		protocol = normalizedRuleProtocol(protocol)
		source = normalizedRuleSource(source)
		if strings.Contains(strings.ToLower(target), "(v6)") && source == "0.0.0.0/0" {
			source = "::/0"
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
	args, err := s.ufwRuleArgs([]string{rule.Action}, rule)
	if err != nil {
		return err
	}
	_, err = s.runner.Run(ctx, system.CommandRequest{
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
	args, err := s.ufwRuleArgs([]string{"--force", "delete", rule.Action}, rule)
	if err != nil {
		return err
	}
	_, err = s.runner.Run(ctx, system.CommandRequest{
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
	return err
}

func (s *FirewallService) ufwRuleArgs(prefix []string, rule *models.PanelFirewallRule) ([]string, error) {
	source := normalizedRuleSource(rule.Source)
	if source == "" || source == "0.0.0.0/0" || source == "::/0" {
		source = "any"
	}
	proto := normalizedRuleProtocol(rule.Protocol)
	port := strings.TrimSpace(rule.Port)
	args := append([]string{}, prefix...)

	if proto == "icmp" {
		if port != "" && port != "any" {
			return nil, fmt.Errorf("icmp firewall rules cannot include a port")
		}
		args = append(args, "from", source, "to", "any", "proto", "icmp")
		return args, nil
	}

	args = append(args, "from", source, "to", "any")
	if port != "" && port != "any" {
		args = append(args, "port", port)
	}
	if proto != "" && proto != "any" {
		args = append(args, "proto", proto)
	}
	return args, nil
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
			return err
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
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.IPTablesBinary,
		Args:        s.iptablesArgs("-D", rule),
		Timeout:     20 * time.Second,
		AuditAction: "firewall.iptables.delete",
		ActorUserID: actor,
		IP:          ip,
	})
	if err != nil {
		return err
	}
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
	v = strings.ReplaceAll(v, "(v6)", "")
	v = strings.TrimSpace(v)
	if v == "" {
		return "tcp"
	}
	switch v {
	case "tcp", "udp", "icmp", "any":
		return v
	}
	if strings.HasPrefix(v, "tcp") {
		return "tcp"
	}
	if strings.HasPrefix(v, "udp") {
		return "udp"
	}
	if strings.HasPrefix(v, "icmp") {
		return "icmp"
	}
	return v
}

func normalizedRuleSource(value string) string {
	v := strings.TrimSpace(value)
	switch strings.ToLower(v) {
	case "", "0.0.0.0/0", "::/0", "anywhere", "anywhere (v6)", "any":
		return "0.0.0.0/0"
	}
	return v
}

func normalizedPortSpec(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func portSpecIncludes(spec, target string) bool {
	spec = normalizedPortSpec(spec)
	target = strings.TrimSpace(target)
	if spec == "" || spec == "any" {
		return true
	}
	if spec == target {
		return true
	}
	targetPort, err := strconv.Atoi(target)
	if err != nil {
		return false
	}
	for _, sep := range []string{":", "-"} {
		if !strings.Contains(spec, sep) {
			continue
		}
		parts := strings.SplitN(spec, sep, 2)
		if len(parts) != 2 {
			continue
		}
		start, startErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, endErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if startErr != nil || endErr != nil {
			continue
		}
		if start > end {
			start, end = end, start
		}
		return targetPort >= start && targetPort <= end
	}
	return false
}

func sshdBinaryPath() string {
	for _, candidate := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd", "sshd"} {
		if filepath.IsAbs(candidate) {
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return candidate
			}
			continue
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	return ""
}

func ensureRuntimeDir(path string, mode os.FileMode) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || !filepath.IsAbs(clean) {
		return fmt.Errorf("invalid runtime directory: %s", path)
	}
	if err := os.MkdirAll(clean, mode); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(clean, 0, 0); err != nil {
			return err
		}
	}
	return os.Chmod(clean, mode)
}

func addSSHDPorts(ports map[string]struct{}, output string) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.EqualFold(fields[0], "port") {
			addPort(ports, fields[1])
		}
	}
}

func addSSHConfigPorts(ports map[string]struct{}) {
	files := []string{"/etc/ssh/sshd_config"}
	if matches, err := filepath.Glob("/etc/ssh/sshd_config.d/*.conf"); err == nil {
		files = append(files, matches...)
	}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "Port") {
				addPort(ports, fields[1])
			}
		}
	}
}

func addPort(ports map[string]struct{}, value string) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return
	}
	ports[strconv.Itoa(port)] = struct{}{}
}
