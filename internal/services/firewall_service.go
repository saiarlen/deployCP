package services

import (
	"context"
	"fmt"
	"net"
	"os/exec"
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
