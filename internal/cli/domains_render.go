package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// The parts of a project domain (GET /v1/projects/{project}/domains) the
// CLI shows people.
type dnsRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Host  string `json:"host"`
	Value string `json:"value"`
}

type projectDomain struct {
	Domain      string      `json:"domain"`
	Project     string      `json:"project"`
	RedirectTo  string      `json:"redirectTo"`
	State       string      `json:"state"`
	Reason      string      `json:"reason"`
	Records     []dnsRecord `json:"records"`
	Alternative []dnsRecord `json:"alternative"`
	Registrar   *struct {
		Name    string `json:"name"`
		Where   string `json:"where"`
		Parked  bool   `json:"parked"`
		Parking string `json:"parking"`
		Remove  string `json:"remove"`
	} `json:"registrar"`
	ManagedBy   string `json:"managedBy"`
	Certificate *struct {
		State  string `json:"state"`
		Issuer string `json:"issuer"`
	} `json:"certificate"`
	Checked string `json:"checked"`
	URL     string `json:"url"`
}

// isDomain: the object is a project domain's status.
func isDomain(probe map[string]json.RawMessage) bool {
	for _, k := range []string{"domain", "state", "records", "checked", "url"} {
		if _, ok := probe[k]; !ok {
			return false
		}
	}
	return true
}

// renderDomainShapes prints domains list, domains check and domains add
// answers for people. It reports whether raw was one of them.
func (a *app) renderDomainShapes(out io.Writer, raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var probes []map[string]json.RawMessage
		if json.Unmarshal(raw, &probes) != nil || len(probes) == 0 || !isDomain(probes[0]) {
			return false
		}
		var ds []projectDomain
		if json.Unmarshal(raw, &ds) != nil {
			return false
		}
		for i, d := range ds {
			if i > 0 {
				fmt.Fprintln(out)
			}
			a.renderDomain(out, d)
		}
		return true
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	if isDomain(probe) {
		var d projectDomain
		if json.Unmarshal(raw, &d) != nil {
			return false
		}
		a.renderDomain(out, d)
		return true
	}
	// domains add / remove: {applied, change, plan, domain}.
	if _, ok := probe["applied"]; !ok {
		return false
	}
	var dp map[string]json.RawMessage
	if dr, ok := probe["domain"]; !ok || json.Unmarshal(dr, &dp) != nil || !isDomain(dp) {
		return false
	}
	var ch struct {
		Applied bool `json:"applied"`
		Change  *struct {
			ID      string `json:"id"`
			Project string `json:"project"`
			Version int    `json:"version"`
			Plan    struct {
				Summary string `json:"summary"`
			} `json:"plan"`
		} `json:"change"`
		Domain projectDomain `json:"domain"`
	}
	if json.Unmarshal(raw, &ch) != nil {
		return false
	}
	if ch.Applied && ch.Change != nil {
		c := ch.Change
		fmt.Fprintf(out, "%s %s  %s (project %s, version %d)\n", a.paint("✓ applied", green), c.ID, c.Plan.Summary, c.Project, c.Version)
	} else {
		fmt.Fprintln(out, "No changes. The box already matches.")
	}
	fmt.Fprintln(out)
	a.renderDomain(out, ch.Domain)
	if ch.Applied && ch.Change != nil {
		fmt.Fprintf(out, "\n%s undo with %s\n", a.paint("→", amber), a.paint("tiffin undo "+ch.Change.ID, bold))
	}
	return true
}

func stateWords(state string) (string, string) {
	switch state {
	case "live":
		return "live", green
	case "issuing":
		return "getting a certificate", amber
	case "error":
		return "error", red
	}
	return "waiting for DNS", amber
}

func (a *app) renderDomain(out io.Writer, d projectDomain) {
	word, color := stateWords(d.State)
	head := fmt.Sprintf("%s  %s", a.paint(d.Domain, bold), a.paint(word, color))
	if d.RedirectTo != "" {
		head += a.paint("  (redirects to "+d.RedirectTo+")", dim)
	}
	fmt.Fprintln(out, head)
	switch d.State {
	case "live":
		line := "  " + d.URL
		if c := d.Certificate; c != nil && c.Issuer != "" {
			line += a.paint("  certificate: "+c.Issuer, dim)
		}
		fmt.Fprintln(out, line)
		return
	case "issuing":
		fmt.Fprintln(out, "  It points at this box; the certificate usually takes under a minute.")
		return
	}
	if d.Reason != "" {
		fmt.Fprintf(out, "  %s\n", sentenceCase(d.Reason))
	}
	if d.State == "waiting_for_dns" && len(d.Records) > 0 {
		if rg := d.Registrar; rg != nil && rg.Parked {
			fmt.Fprintf(out, "  %s In %s › %s, delete %s. Then add:\n", a.paint("Parked:", amber), rg.Name, rg.Where, rg.Remove)
		} else if rg != nil {
			fmt.Fprintf(out, "  In %s › %s, add:\n", rg.Name, rg.Where)
		} else {
			fmt.Fprintln(out, "  Add at your DNS host:")
		}
		printRecords(out, d.Records)
		if len(d.Alternative) > 0 {
			fmt.Fprintln(out, "  Or instead:")
			printRecords(out, d.Alternative)
		}
		if d.ManagedBy != "" {
			fmt.Fprintf(out, "  %s holds this zone: add the domain again with --create-records to set them for you.\n", d.ManagedBy)
		}
	}
	if d.Checked != "" {
		fmt.Fprintf(out, "  %s. Check now: %s\n", d.Checked, a.paint("tiffin domains check "+d.Project+" "+d.Domain, bold))
	}
}

func printRecords(out io.Writer, recs []dnsRecord) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range recs {
		fmt.Fprintf(tw, "    %s\t%s\t%s\t(%s)\n", r.Type, orDefault(r.Host, r.Name), r.Value, r.Name)
	}
	tw.Flush()
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// renderPlanRecords prints the DNS records a planned change needs (the
// 428 answer of domains add), before anything changed.
func (a *app) renderPlanRecords(out io.Writer, raw []byte) {
	var p struct {
		Records []dnsRecord `json:"records"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Records) == 0 {
		return
	}
	fmt.Fprintln(out, "DNS records to add at your DNS host (you can add them now; the box watches for them):")
	printRecords(out, p.Records)
}
