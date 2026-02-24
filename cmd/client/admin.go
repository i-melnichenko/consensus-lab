// Package main – admin subcommand: live monitoring table rendered with bubbletea + lipgloss.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	adminpb "github.com/i-melnichenko/consensus-lab/pkg/proto/adminv1"
)

const adminRefreshInterval = 500 * time.Millisecond

// ---- Data types -------------------------------------------------------------

type adminConn struct {
	addr   string
	conn   *grpc.ClientConn
	client adminpb.AdminServiceClient
}

type adminRow struct {
	addr          string
	nodeID        string
	consensus     string
	role          string
	status        string
	leaderID      string
	term          int64
	commit        int64
	applied       int64
	lastLog       int64
	lastLogTerm   int64
	snapshot      int64
	cfg           string
	peers         string
	lastAppliedAt string
	err           string
}

// ---- Bubbletea messages -----------------------------------------------------

type tickMsg time.Time

type rowsMsg struct {
	rows []adminRow
	ts   time.Time
}

// ---- Lipgloss styles --------------------------------------------------------

type uiStyles struct {
	dotHealthy   lipgloss.Style
	dotDegraded  lipgloss.Style
	dotUnavail   lipgloss.Style
	dotUnknown   lipgloss.Style
	dotSelected  lipgloss.Style
	addr         lipgloss.Style
	nodeNorm     lipgloss.Style
	nodeLead     lipgloss.Style
	roleLeader   lipgloss.Style
	roleCand     lipgloss.Style
	roleFollow   lipgloss.Style
	leaderSelf   lipgloss.Style
	leaderOther  lipgloss.Style
	leaderNone   lipgloss.Style
	termVal      lipgloss.Style
	metric       lipgloss.Style
	timeVal      lipgloss.Style
	cfgVal       lipgloss.Style
	tableHeader  lipgloss.Style
	appHeader    lipgloss.Style
	tsStyle      lipgloss.Style
	footer       lipgloss.Style
	divider      lipgloss.Style
	alertsHdr    lipgloss.Style
	errorDot     lipgloss.Style
	errorKindSty lipgloss.Style
	peerLabel    lipgloss.Style
	peerValue    lipgloss.Style
	sumDim       lipgloss.Style
	sumHealthy   lipgloss.Style
	sumErrors    lipgloss.Style
	sumLeader    lipgloss.Style
	sumCand      lipgloss.Style
	ldrMissing   lipgloss.Style
}

var styles = buildStyles()

func buildStyles() uiStyles {
	// Color codes mirror the original ANSI constants:
	// "1"=red  "2"=green  "3"=yellow  "4"=blue  "5"=magenta  "6"=cyan
	// "7"=white  "8"=bright-black
	return uiStyles{
		dotHealthy:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")),
		dotDegraded:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		dotUnavail:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
		dotUnknown:   lipgloss.NewStyle().Faint(true),
		dotSelected:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		addr:         lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("6")),
		nodeNorm:     lipgloss.NewStyle(),
		nodeLead:     lipgloss.NewStyle().Bold(true),
		roleLeader:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")),
		roleCand:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		roleFollow:   lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		leaderSelf:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		leaderOther:  lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		leaderNone:   lipgloss.NewStyle().Faint(true),
		termVal:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		metric:       lipgloss.NewStyle().Faint(true),
		timeVal:      lipgloss.NewStyle().Faint(true),
		cfgVal:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5")),
		tableHeader:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("7")).Background(lipgloss.Color("8")),
		appHeader:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		tsStyle:      lipgloss.NewStyle().Faint(true),
		footer:       lipgloss.NewStyle().Faint(true),
		divider:      lipgloss.NewStyle().Faint(true),
		alertsHdr:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		errorDot:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
		errorKindSty: lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		peerLabel:    lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		peerValue:    lipgloss.NewStyle().Faint(true),
		sumDim:       lipgloss.NewStyle().Faint(true),
		sumHealthy:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		sumErrors:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		sumLeader:    lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		sumCand:      lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		ldrMissing:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
	}
}

// ---- Column widths ----------------------------------------------------------

type adminColWidths struct {
	addr   int
	node   int
	role   int
	leader int
}

// adminColumnsForWidth computes variable column widths to fill contentWidth.
// Returns (cols, showAat) where showAat indicates the A_AT column is visible.
func adminColumnsForWidth(rows []adminRow, contentWidth int) (adminColWidths, bool) {
	col := adminColWidths{addr: 12, node: 5, role: 6, leader: 4}

	maxAddr := len("ADDR")
	maxNode := len("NODE")
	maxRole := len("ROLE")
	maxLeader := len("LEADER")
	for _, r := range rows {
		if len(r.addr) > maxAddr {
			maxAddr = len(r.addr)
		}
		if len(r.nodeID) > maxNode {
			maxNode = len(r.nodeID)
		}
		if len(r.role) > maxRole {
			maxRole = len(r.role)
		}
		if len(r.leaderID) > maxLeader {
			maxLeader = len(r.leaderID)
		}
	}
	col.addr = clampInt(maxAddr, 8, 14)
	col.node = clampInt(maxNode, 4, 7)
	col.role = clampInt(maxRole, 6, 8)
	col.leader = clampInt(maxLeader, 2, 6)

	baseVar := col.addr + col.node + col.role + col.leader
	showAat := contentWidth >= 96
	// Fixed chars (without A_AT): ST(2)+TERM(4)+CMT(5)+APL(5)+LOG(5)+SNAP(5)+CFG(3)+10 spaces = 39
	// Fixed chars (with A_AT):    add A_AT(8)+1 space = 48
	fixed := 39
	if showAat {
		fixed = 48
	}
	targetVar := contentWidth - fixed
	if targetVar <= 0 {
		return col, showAat
	}

	extra := targetVar - baseVar
	if extra < 0 {
		// Narrow terminal: shrink variable columns down to minimums.
		type shrinkEntry struct {
			cur *int
			min int
		}
		deficit := -extra
		for _, s := range []shrinkEntry{
			{&col.addr, 4},
			{&col.leader, 2},
			{&col.node, 4},
			{&col.role, 6},
		} {
			if deficit == 0 {
				break
			}
			capacity := *s.cur - s.min
			if capacity <= 0 {
				continue
			}
			delta := minInt(deficit, capacity)
			*s.cur -= delta
			deficit -= delta
		}
		return col, showAat
	}
	// Wider terminal: stretch ADDR up to 8 extra chars.
	col.addr += minInt(extra, 8)
	return col, showAat
}

// ---- Cell renderers ---------------------------------------------------------
// Each renderer pads the raw value to `width` visible chars, then applies a
// lipgloss style.  No padding is added inside the style itself so column-width
// math stays exact.

func renderStatusDot(status, errStr string, selected bool) string {
	if selected {
		return styles.dotSelected.Render("▶") + " "
	}
	if errStr != "" {
		return styles.dotUnavail.Render("●") + " "
	}
	switch status {
	case "healthy":
		return styles.dotHealthy.Render("●") + " "
	case "degraded":
		return styles.dotDegraded.Render("●") + " "
	case "unavailable":
		return styles.dotUnavail.Render("●") + " "
	default:
		return styles.dotUnknown.Render("·") + " "
	}
}

func renderAddrCell(s string, width int) string {
	return styles.addr.Render(fmt.Sprintf("%-*s", width, shorten(s, width)))
}

func renderNodeCell(s string, width int, role string) string {
	padded := fmt.Sprintf("%-*s", width, shorten(s, width))
	if role == "leader" {
		return styles.nodeLead.Render(padded)
	}
	return padded
}

func renderRoleCell(s string, width int, role string) string {
	padded := fmt.Sprintf("%-*s", width, shorten(s, width))
	switch role {
	case "leader":
		return styles.roleLeader.Render(padded)
	case "candidate":
		return styles.roleCand.Render(padded)
	case "follower":
		return styles.roleFollow.Render(padded)
	default:
		return padded
	}
}

func renderLeaderCell(s string, width int, role string) string {
	padded := fmt.Sprintf("%-*s", width, shorten(s, width))
	if s == "" {
		return styles.leaderNone.Render(padded)
	}
	if role == "leader" {
		return styles.leaderSelf.Render(padded)
	}
	return styles.leaderOther.Render(padded)
}

func renderTermCell(v int64, width int) string {
	return styles.termVal.Render(fmt.Sprintf("%*d", width, v))
}

func renderMetricCell(v int64, width int) string {
	return styles.metric.Render(fmt.Sprintf("%*d", width, v))
}

func renderTimeCell(s string, width int) string {
	return styles.timeVal.Render(fmt.Sprintf("%-*s", width, shorten(s, width)))
}

func renderCFGCell(s string, width int) string {
	padded := fmt.Sprintf("%-*s", width, shorten(s, width))
	if strings.TrimSpace(s) == "" {
		return padded
	}
	return styles.cfgVal.Render(padded)
}

// makeTableRow builds the single-line string for one admin row.
// selected=true replaces the status dot with the cursor arrow ▶.
func makeTableRow(r adminRow, cols adminColWidths, showAat, selected bool) string {
	dot := renderStatusDot(r.status, r.err, selected)

	if r.err != "" {
		dash := "-"
		base := dot + " " +
			renderAddrCell(r.addr, cols.addr) +
			" " + fmt.Sprintf("%-*s", cols.node, dash) +
			" " + fmt.Sprintf("%-*s", cols.role, dash) +
			" " + fmt.Sprintf("%-*s", cols.leader, dash) +
			" " + fmt.Sprintf("%4s", dash) +
			" " + fmt.Sprintf("%5s", dash) +
			" " + fmt.Sprintf("%5s", dash) +
			" " + fmt.Sprintf("%5s", dash) +
			" " + fmt.Sprintf("%5s", dash)
		if showAat {
			return base + " " + fmt.Sprintf("%-8s", dash) + " " + fmt.Sprintf("%-3s", dash)
		}
		return base + " " + fmt.Sprintf("%-3s", dash)
	}

	base := dot + " " +
		renderAddrCell(r.addr, cols.addr) +
		" " + renderNodeCell(r.nodeID, cols.node, r.role) +
		" " + renderRoleCell(r.role, cols.role, r.role) +
		" " + renderLeaderCell(r.leaderID, cols.leader, r.role) +
		" " + renderTermCell(r.term, 4) +
		" " + renderMetricCell(r.commit, 5) +
		" " + renderMetricCell(r.applied, 5) +
		" " + renderMetricCell(r.lastLog, 5) +
		" " + renderMetricCell(r.snapshot, 5)
	if showAat {
		return base + " " + renderTimeCell(r.lastAppliedAt, 8) + " " + renderCFGCell(r.cfg, 3)
	}
	return base + " " + renderCFGCell(r.cfg, 3)
}

// renderHeader returns the styled table header line padded to contentWidth.
func renderHeader(cols adminColWidths, showAat bool, contentWidth int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-2s", "ST")
	fmt.Fprintf(&b, " %-*s", cols.addr, headerLabel("ADDR", cols.addr))
	fmt.Fprintf(&b, " %-*s", cols.node, headerLabel("NODE", cols.node))
	fmt.Fprintf(&b, " %-*s", cols.role, headerLabel("ROLE", cols.role))
	fmt.Fprintf(&b, " %-*s", cols.leader, headerLabel("LEADER", cols.leader))
	fmt.Fprintf(&b, " %4s", "TERM")
	fmt.Fprintf(&b, " %5s", "CMT")
	fmt.Fprintf(&b, " %5s", "APL")
	fmt.Fprintf(&b, " %5s", "LOG")
	fmt.Fprintf(&b, " %5s", "SNAP")
	if showAat {
		fmt.Fprintf(&b, " %-8s", "A_AT")
	}
	fmt.Fprintf(&b, " %-3s", "CFG")
	return styles.tableHeader.Width(contentWidth).MaxWidth(contentWidth).Render(b.String())
}

// renderSummary returns the "[N total] [N healthy] ..." line.
func renderSummary(rows []adminRow) string {
	total := len(rows)
	healthy, errorsN, leaders, candidates := 0, 0, 0, 0
	for _, r := range rows {
		if r.err != "" {
			errorsN++
			continue
		}
		if r.status == "healthy" {
			healthy++
		}
		switch r.role {
		case "leader":
			leaders++
		case "candidate":
			candidates++
		}
	}
	bracket := func(st lipgloss.Style, label string, n int) string {
		d := styles.sumDim
		return d.Render("[") + st.Render(fmt.Sprintf("%d", n)) + d.Render(" "+label+"]")
	}
	return strings.Join([]string{
		bracket(lipgloss.NewStyle(), "total", total),
		bracket(styles.sumHealthy, "healthy", healthy),
		bracket(styles.sumErrors, "errors", errorsN),
		bracket(styles.sumLeader, "leader", leaders),
		bracket(styles.sumCand, "candidate", candidates),
	}, " ")
}

// buildAlertLines returns alert lines (without the divider/header).
func buildAlertLines(rows []adminRow, contentWidth int) []string {
	var lines []string
	if leaderMissing, quorum, healthy := detectLeaderMissing(rows); leaderMissing {
		lines = append(lines, fmt.Sprintf("%s healthy=%d quorum=%d (%s)",
			styles.ldrMissing.Render("LEADER_MISSING"),
			healthy, quorum,
			"election in progress or stalled",
		))
	}
	for _, r := range rows {
		if r.err == "" {
			continue
		}
		summary := shorten(errorSummary(r.err), maxInt(20, contentWidth-28))
		lines = append(lines, fmt.Sprintf("%s %s %s %s",
			styles.errorDot.Render("●"),
			r.addr,
			styles.errorKindSty.Render(errorKind(r.err)),
			summary,
		))
	}
	return lines
}

// ---- Bubbletea model --------------------------------------------------------

type adminModel struct {
	rows       []adminRow
	ts         time.Time
	conns      []adminConn
	timeout    time.Duration
	width      int
	height     int
	cursor     int
	scrollOff  int
	selectedID string
	showAat    bool
	cols       adminColWidths
}

func newAdminModel(conns []adminConn, timeout time.Duration) adminModel {
	return adminModel{
		conns:   conns,
		timeout: timeout,
		width:   120,
		height:  40,
	}
}

func (m adminModel) Init() tea.Cmd {
	// Only fire the initial poll.  rowsMsg schedules the first tick, which
	// in turn fires the next poll — keeping exactly one poll in flight at a
	// time and preventing out-of-order rowsMsg overwrites.
	return m.pollCmd()
}

func (m adminModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcCols()
		return m, nil

	case tickMsg:
		// Tick fires a poll; the next tick is scheduled when the poll returns.
		return m, m.pollCmd()

	case rowsMsg:
		m.rows = msg.rows
		m.ts = msg.ts
		m.recalcCols()
		m.restoreSelection()
		tickFn := func(t time.Time) tea.Msg { return tickMsg(t) }
		return m, tea.Tick(adminRefreshInterval, tickFn)

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			m.moveCursor(-1)
		case "down", "j":
			m.moveCursor(1)
		}
	}
	return m, nil
}

func (m adminModel) View() string {
	contentWidth := m.width - 2
	if contentWidth <= 0 {
		contentWidth = 80
	}

	var b strings.Builder

	// Title line
	b.WriteString("  ")
	b.WriteString(styles.appHeader.Render("Admin view"))
	b.WriteString("  ")
	b.WriteString(styles.tsStyle.Render(m.ts.Format(time.RFC3339)))
	b.WriteString("\n")

	// Summary line
	b.WriteString(renderSummary(m.rows))
	b.WriteString("\n")

	// Blank
	b.WriteString("\n")

	// Table header
	b.WriteString(renderHeader(m.cols, m.showAat, contentWidth))
	b.WriteString("\n")

	// Rows (viewport-clipped)
	visRows := m.visibleRowCount()
	start := m.scrollOff
	end := minInt(start+visRows, len(m.rows))
	for i := start; i < end; i++ {
		b.WriteString(makeTableRow(m.rows[i], m.cols, m.showAat, i == m.cursor))
		b.WriteString("\n")
	}

	// Blank + peers pane
	b.WriteString("\n")
	peers := "-"
	if m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].peers != "" {
		peers = m.rows[m.cursor].peers
	}
	b.WriteString("  ")
	b.WriteString(styles.peerLabel.Render("peers:"))
	b.WriteString(" ")
	b.WriteString(styles.peerValue.Render(shorten(peers, maxInt(10, contentWidth-12))))
	b.WriteString("\n")

	// Alerts section
	alertLines := buildAlertLines(m.rows, contentWidth)
	if len(alertLines) > 0 {
		b.WriteString(styles.divider.Render(strings.Repeat("-", contentWidth)))
		b.WriteString("\n")
		b.WriteString(styles.alertsHdr.Render("Alerts"))
		b.WriteString("\n")
		for _, line := range alertLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	// Footer
	b.WriteString("\n")
	b.WriteString("  ")
	b.WriteString(styles.footer.Render("Ctrl+C to exit"))

	// Pad to terminal height with blank lines.
	// Bubbletea's diff renderer writes the new frame from the top and does
	// not always erase lines below the last line of the new frame.  When
	// the alerts section disappears (shorter frame), ghost lines remain on
	// screen.  Filling to m.height forces those old lines to be overwritten
	// with blanks on every render cycle.
	out := b.String()
	if m.height > 0 {
		lines := strings.Split(out, "\n")
		for len(lines) < m.height {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}
	return out
}

// ---- Model helpers ----------------------------------------------------------

func (m *adminModel) recalcCols() {
	contentWidth := m.width - 2
	if contentWidth <= 0 {
		contentWidth = 80
	}
	m.cols, m.showAat = adminColumnsForWidth(m.rows, contentWidth)
}

func (m *adminModel) restoreSelection() {
	if m.selectedID == "" {
		if len(m.rows) > 0 {
			m.cursor = 0
			m.selectedID = m.rows[0].nodeID
		}
		return
	}
	for i, r := range m.rows {
		if r.nodeID == m.selectedID {
			m.cursor = i
			m.clampScroll()
			return
		}
	}
	// Previously selected node not in new rows; sync from current cursor.
	if m.cursor >= len(m.rows) {
		m.cursor = maxInt(0, len(m.rows)-1)
	}
	if len(m.rows) > 0 {
		m.selectedID = m.rows[m.cursor].nodeID
	}
}

func (m *adminModel) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = clampInt(m.cursor+delta, 0, len(m.rows)-1)
	m.clampScroll()
	m.selectedID = m.rows[m.cursor].nodeID
}

func (m *adminModel) clampScroll() {
	visRows := m.visibleRowCount()
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	} else if m.cursor >= m.scrollOff+visRows {
		m.scrollOff = m.cursor - visRows + 1
	}
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}
}

func (m adminModel) visibleRowCount() int {
	// Overhead: title(1)+summary(1)+blank(1)+header(1)+blank(1)+peers(1)+blank(1)+footer(1) = 8
	// Plus worst-case alerts: divider(1)+alertsHdr(1)+N lines
	return maxInt(2, m.height-9)
}

func (m adminModel) pollCmd() tea.Cmd {
	conns := m.conns
	timeout := m.timeout
	return func() tea.Msg {
		rows, ts := pollAdminRows(context.Background(), conns, timeout)
		return rowsMsg{rows: rows, ts: ts}
	}
}

// ---- Pure logic (unchanged from original) -----------------------------------

func cmdAdmin(addrs []string, timeout time.Duration) error {
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses provided")
	}
	conns, err := openAdminConns(addrs)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range conns {
			_ = c.conn.Close()
		}
	}()

	p := tea.NewProgram(newAdminModel(conns, timeout), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func openAdminConns(addrs []string) ([]adminConn, error) {
	conns := make([]adminConn, 0, len(addrs))
	for _, addr := range addrs {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			for _, c := range conns {
				_ = c.conn.Close()
			}
			return nil, fmt.Errorf("dial admin %s: %w", addr, err)
		}
		conns = append(conns, adminConn{
			addr:   addr,
			conn:   conn,
			client: adminpb.NewAdminServiceClient(conn),
		})
	}
	return conns, nil
}

func pollAdminRows(ctx context.Context, conns []adminConn, timeout time.Duration) ([]adminRow, time.Time) {
	rows := make([]adminRow, len(conns))
	var wg sync.WaitGroup
	wg.Add(len(conns))

	for i, c := range conns {
		go func(i int, c adminConn) {
			defer wg.Done()

			row := adminRow{addr: c.addr}

			reqCtx, cancel := context.WithTimeout(ctx, timeout)
			resp, err := c.client.GetNodeInfo(reqCtx, &adminpb.GetNodeInfoRequest{})
			cancel()
			if err != nil {
				row.err = err.Error()
				rows[i] = row
				return
			}
			node := resp.GetNode()
			if node == nil {
				row.err = "empty node info"
				rows[i] = row
				return
			}

			row.nodeID = node.GetNodeId()
			row.consensus = trimEnum(node.GetConsensusType().String(), "CONSENSUS_TYPE_")
			row.role = trimEnum(node.GetRole().String(), "NODE_ROLE_")
			row.status = trimEnum(node.GetStatus().String(), "NODE_STATUS_")
			row.peers = formatPeerList(node.GetPeers())

			if raft := node.GetRaft(); raft != nil {
				row.leaderID = raft.GetLeaderId()
				row.term = raft.GetTerm()
				row.commit = raft.GetCommitIndex()
				row.applied = raft.GetLastApplied()
				row.lastLog = raft.GetLastLogIndex()
				row.lastLogTerm = raft.GetLastLogTerm()
				row.snapshot = raft.GetSnapshotLastIndex()
				row.lastAppliedAt = formatTS(raft.GetLastAppliedAt())
				row.cfg = formatCFG(int(raft.GetQuorumSize()), len(raft.GetClusterMembers()))
			}

			rows[i] = row
		}(i, c)
	}

	wg.Wait()

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].nodeID == rows[j].nodeID {
			return rows[i].addr < rows[j].addr
		}
		if rows[i].nodeID == "" {
			return false
		}
		if rows[j].nodeID == "" {
			return true
		}
		return rows[i].nodeID < rows[j].nodeID
	})

	return rows, time.Now()
}

func formatPeerList(peers []*adminpb.PeerInfo) string {
	if len(peers) == 0 {
		return ""
	}
	items := make([]string, 0, len(peers))
	for _, p := range peers {
		if p == nil {
			continue
		}
		id := strings.TrimSpace(p.GetNodeId())
		addr := strings.TrimSpace(p.GetAddress())
		if id != "" {
			items = append(items, id)
			continue
		}
		if addr != "" {
			items = append(items, addr)
		}
	}
	sort.Strings(items)
	return strings.Join(items, ",")
}

func formatTS(ts interface{ AsTime() time.Time }) string {
	if ts == nil {
		return ""
	}
	t := ts.AsTime()
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04:05")
}

func formatCFG(quorumSize, members int) string {
	if quorumSize <= 0 && members <= 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", quorumSize, members)
}

func errorKind(err string) string {
	switch {
	case strings.Contains(err, "code = Unavailable"):
		return "Unavailable"
	case strings.Contains(err, "code = Unimplemented"):
		return "Unimplemented"
	case strings.Contains(err, "code = DeadlineExceeded"):
		return "Timeout"
	default:
		return "Error"
	}
}

func errorSummary(err string) string {
	err = strings.TrimSpace(err)
	err = strings.ReplaceAll(err, "\n", " ")
	err = strings.Join(strings.Fields(err), " ")
	return err
}

func detectLeaderMissing(rows []adminRow) (bool, int, int) {
	quorum := 0
	healthy := 0
	hasLeader := false

	for _, r := range rows {
		if r.err != "" {
			continue
		}
		if r.role == "leader" || r.leaderID != "" {
			hasLeader = true
		}
		if r.status == "healthy" {
			healthy++
		}
		if q, _, ok := parseCFG(r.cfg); ok && q > quorum {
			quorum = q
		}
	}

	if quorum == 0 {
		return false, 0, healthy
	}
	if healthy < quorum {
		return false, quorum, healthy
	}
	return !hasLeader, quorum, healthy
}

func parseCFG(s string) (int, int, bool) {
	if s == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var q, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &q); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, 0, false
	}
	return q, m, true
}

func trimEnum(s, prefix string) string {
	s = strings.TrimPrefix(s, prefix)
	return strings.ToLower(s)
}

func shorten(s string, n int) string {
	if n <= 0 {
		return s
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func headerLabel(label string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(label) <= width {
		return label
	}
	switch label {
	case "LEADER":
		if width >= 3 {
			return "LDR"
		}
	case "ADDR":
		if width >= 2 {
			return "AD"
		}
	case "NODE":
		if width >= 2 {
			return "ND"
		}
	case "ROLE":
		if width >= 2 {
			return "RL"
		}
	}
	return label[:width]
}
