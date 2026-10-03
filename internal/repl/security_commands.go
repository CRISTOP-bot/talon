package repl

import (
	"fmt"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/legal"
	"github.com/CRISTOP-bot/talon/internal/privacy"
	"github.com/CRISTOP-bot/talon/internal/sandbox"
)

// cmdSecurity reports the controls in force. It reads state only: changing the
// posture means editing the config, so what the user sees is what runs.
func (r *REPL) cmdSecurity(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "network":
			r.printNetworkPolicy()
			return
		case "secrets":
			r.printSecretPolicy()
			return
		}
	}
	if r.posture == nil {
		r.pr.Failure("security posture is not available")
		return
	}
	r.pr.Printf("%s", r.posture.Summary())
}

// printNetworkPolicy shows where Talon is allowed to connect.
func (r *REPL) printNetworkPolicy() {
	p := r.posture
	if p == nil {
		r.pr.Failure("security posture is not available")
		return
	}
	r.pr.Blank()
	r.pr.Line("Network policy")
	r.pr.Muted(fmt.Sprintf("  mode             %s", p.Network.Mode))
	r.pr.Muted(fmt.Sprintf("  allowed hosts    %s", listOrNone(p.Network.AllowedHosts)))
	r.pr.Muted(fmt.Sprintf("  http             %s", enabledLabel(p.Network.AllowHTTP)))
	r.pr.Muted(fmt.Sprintf("  loopback         %s", enabledLabel(p.Network.AllowLoopback)))
	r.pr.Muted(fmt.Sprintf("  private IPs      %s", enabledLabel(p.Network.AllowPrivateIPs)))
	r.pr.Muted("  metadata hosts   always blocked")
	r.pr.Blank()
}

// printSecretPolicy shows what happens to credentials.
func (r *REPL) printSecretPolicy() {
	p := r.posture
	if p == nil {
		r.pr.Failure("security posture is not available")
		return
	}
	r.pr.Blank()
	r.pr.Line("Credential handling")
	r.pr.Muted(fmt.Sprintf("  redaction        %s", enabledLabel(p.Config.Security.SecretRedaction)))
	r.pr.Muted(fmt.Sprintf("  sensitive files  %s", p.Sensitive.SensitiveFiles))
	r.pr.Muted(fmt.Sprintf("  content          %s", p.Sensitive.ContentFindings))
	r.pr.Muted(fmt.Sprintf("  child env        %s", listOrNone(r.allowedChildEnv())))
	if p.Privacy.Enabled {
		r.pr.Muted("  note             privacy mode: nothing is written to disk")
	}
	r.pr.Blank()
}

// cmdSandbox reports what the kernel can enforce right now.
func (r *REPL) cmdSandbox() {
	status := r.postureSandboxStatus()
	r.pr.Blank()
	r.pr.Line("Sandbox")
	r.pr.Printf("%s", strings.ReplaceAll(status.String(), "\n", "\n  "))
	r.pr.Blank()
}

func (r *REPL) postureSandboxStatus() sandbox.Status {
	if r.posture == nil {
		return sandbox.Detect()
	}
	return r.posture.SandboxStatus
}

// cmdTerms shows the Terms, their history and the acceptance state.
func (r *REPL) cmdTerms(args []string) {
	doc, err := legal.CurrentTerms()
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "current":
		r.printTerms(doc, "")
	case "accept":
		r.acceptLegal(legal.KindTerms, "terms of use")
	case "history":
		r.printAcceptanceHistory(legal.KindTerms)
	default:
		if v, verr := legal.TermsByVersion(sub); verr == nil {
			r.printTerms(v, "")
			return
		}
		r.pr.Failure(fmt.Sprintf("unknown argument %q — try /terms, /terms accept, /terms history or /terms <version>", sub))
	}
}

func (r *REPL) printTerms(doc legal.Document, extra string) {
	r.pr.Blank()
	r.pr.Line(fmt.Sprintf("Talon Terms of Use %s", doc.Version))
	if doc.EffectiveAt != "" {
		r.pr.Muted("effective " + doc.EffectiveAt)
	}
	if extra != "" {
		r.pr.Muted(extra)
	}
	r.pr.Blank()
	for _, line := range strings.Split(strings.TrimRight(doc.Body, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			r.pr.Blank()
			continue
		}
		r.pr.Muted("  " + line)
	}
	if blocked, message := r.termsGate(); blocked {
		r.pr.Blank()
		r.pr.Warn(message)
		r.pr.Muted("run /terms accept to record your acceptance")
	}
	r.pr.Blank()
}

// acceptLegal records consent in the local ledger and audits it.
func (r *REPL) acceptLegal(kind, label string) {
	if r.legal == nil {
		r.pr.Failure("the consent ledger is not available; acceptance cannot be recorded")
		return
	}
	doc, err := legal.DocumentByKind(kind)
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	acc, err := r.legal.Accept(doc, "cli")
	if err != nil {
		r.pr.Failure(err.Error())
		return
	}
	if r.posture != nil && r.posture.Audit != nil {
		r.posture.Audit.Consent(acc.Version, kind)
	}
	r.pr.Success(fmt.Sprintf("accepted the %s %s (%s)", label, acc.Version, acc.AcceptedAt.Format("2006-01-02 15:04")))
}

// printAcceptanceHistory lists recorded acceptances.
func (r *REPL) cmdPrivacy(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "current", "":
		default:
			r.pr.Failure(fmt.Sprintf("unknown argument %q — try /privacy or /privacy current", args[0]))
			return
		}
	}
	doc, err := legal.CurrentPrivacy()
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	r.pr.Blank()
	r.pr.Line(fmt.Sprintf("Talon Privacy Notice %s", doc.Version))
	if doc.EffectiveAt != "" {
		r.pr.Muted("effective " + doc.EffectiveAt)
	}
	r.pr.Blank()
	for _, line := range strings.Split(strings.TrimRight(doc.Body, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			r.pr.Blank()
			continue
		}
		r.pr.Muted("  " + line)
	}
	r.printDataInventory()
}

// printDataInventory lists what Talon stores and where.
func (r *REPL) printDataInventory() {
	items := privacy.Inventory(r.opts.Workspace)
	r.pr.Blank()
	r.pr.Line("Data stored on this machine")
	width := 0
	for _, item := range items {
		if n := len(item.Category.Label()); n > width {
			width = n
		}
	}
	for _, item := range items {
		r.pr.Printf("  %-*s  %s\n", width, item.Category.Label(), r.theme.Faint(item.Human()))
	}
	r.pr.Blank()
	r.pr.Muted("nothing leaves this machine except the content sent to your model provider")
	r.pr.Muted("run /data clear <category> to remove a category, or /data clear all")
	r.pr.Blank()
}

// cmdData lists or clears stored data.
func (r *REPL) cmdData(args []string) {
	if len(args) == 0 {
		r.printDataInventory()
		return
	}
	switch args[0] {
	case "list":
		r.printDataInventory()
	case "clear":
		r.clearData(args[1:])
	default:
		r.pr.Failure(fmt.Sprintf("unknown argument %q — try /data list, /data clear <category> or /data clear all", args[0]))
	}
}

// clearData removes stored categories after confirmation.
func (r *REPL) clearData(args []string) {
	if len(args) == 0 {
		r.pr.Failure("say what to clear: /data clear <category> or /data clear all")
		return
	}
	req := privacy.ClearRequest{ProjectRoot: r.opts.Workspace}
	if args[0] != "all" {
		cat, err := privacy.ParseCategory(args[0])
		if err != nil {
			r.pr.Failure(fmt.Sprintf("%q is not a category — try one of: %s",
				args[0], strings.Join(categoryNames(), ", ")))
			return
		}
		req.Categories = []privacy.Category{cat}
	}
	if !r.confirmDeletion(fmt.Sprintf("Permanently delete %s data? This cannot be undone.", args[0])) {
		r.pr.Muted("cancelled")
		return
	}
	plan := privacy.BuildPlan(req)
	if len(plan.Items) == 0 {
		r.pr.Muted("nothing to delete")
		return
	}
	for _, item := range plan.Items {
		if !r.confirmDeletion(fmt.Sprintf("delete %s (%s, %s)?", item.Category.Label(), item.Path, item.Human())) {
			continue
		}
		if err := privacy.Apply(privacy.Plan{Items: []privacy.Item{item}}); err != nil {
			r.pr.Failure(fmt.Sprintf("%s: %v", item.Category.Label(), err))
			continue
		}
		if r.posture != nil && r.posture.Audit != nil {
			r.posture.Audit.DataRemoved(string(item.Category))
		}
		r.pr.Success("removed " + item.Category.Label())
	}
	for _, missing := range plan.Missing {
		r.pr.Muted("not present: " + missing)
	}
}

func categoryNames() []string {
	all := privacy.Categories()
	out := make([]string, 0, len(all))
	for _, c := range all {
		out = append(out, string(c))
	}
	return out
}

// printAcceptanceHistory shows recorded acceptances for a document kind.
func (r *REPL) printAcceptanceHistory(kind string) {
	if r.legal == nil {
		r.pr.Failure("the consent ledger is not available")
		return
	}
	history := r.legal.History(kind)
	if len(history) == 0 {
		r.pr.Muted(fmt.Sprintf("no %s acceptance recorded", kind))
		return
	}
	r.pr.Blank()
	r.pr.Line(kind + " acceptances")
	for _, a := range history {
		r.pr.Muted(fmt.Sprintf("  %s  %s  %s", a.Version, a.AcceptedAt.Format("2006-01-02 15:04"), a.Source))
	}
	r.pr.Blank()
}

// cmdAudit prints recent security decisions.
func (r *REPL) cmdAudit(args []string) {
	log := r.AuditLog()
	if log == nil || !log.Enabled() {
		r.pr.Muted(fmt.Sprintf("the audit log is disabled%s", auditDisabledReason(r)))
		return
	}
	limit := 20
	if len(args) > 0 {
		if n, err := parsePositive(args[0]); err == nil {
			limit = n
		}
	}
	entries, err := audit.Read(log.Path(), limit)
	if err != nil {
		r.pr.Failure(err.Error())
		return
	}
	if len(entries) == 0 {
		r.pr.Muted("the audit log is empty")
		return
	}
	r.pr.Blank()
	r.pr.Line(fmt.Sprintf("Audit log (%s)", log.Path()))
	for _, e := range entries {
		r.pr.Muted("  " + e.String())
	}
	r.pr.Blank()
}

func auditDisabledReason(r *REPL) string {
	if r.posture != nil && r.posture.Privacy.Enabled {
		return " because privacy mode is on"
	}
	if r.posture != nil && !r.posture.Config.Security.AuditEnabled {
		return " by configuration"
	}
	return ""
}

func parsePositive(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
		if n > 500 {
			return 0, fmt.Errorf("too large")
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("zero")
	}
	return n, nil
}

// confirmDeletion asks a yes/no question about an irreversible action. It never
// assumes the answer, and an unreadable stdin counts as "no".
func (r *REPL) confirmDeletion(question string) bool {
	r.pr.Warn(question)
	r.pr.Muted("  [y] delete   [n] keep")
	answer, err := r.readAnswer()
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

func enabledLabel(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
