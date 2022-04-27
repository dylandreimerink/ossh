package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gliderlabs/ssh"
	"golang.org/x/exp/maps"
)

type StatsJSON struct {
	Hosts        []string `json:"hosts"`
	Users        []string `json:"users"`
	Passwords    []string `json:"passwords"`
	Fingerprints []string `json:"fingerprints"`
}

type OSSHServer struct {
	Version     string
	server      *ssh.Server
	shells      map[string]*FakeShell
	syncClients map[string]bool
	Stats       struct {
		Logins struct {
			Attempts map[string]uint
			Failed   map[string]uint
			OK       map[string]uint
		}
		Users        map[string]uint
		Passwords    map[string]uint
		Hosts        map[string]uint
		Fingerprints map[string]uint
		TimeWasted   int
	}
}

func (ossh *OSSHServer) statsJSON() string {
	data := StatsJSON{
		Hosts:        maps.Keys(Server.Stats.Hosts),
		Users:        maps.Keys(Server.Stats.Users),
		Passwords:    maps.Keys(Server.Stats.Passwords),
		Fingerprints: maps.Keys(Server.Stats.Fingerprints),
	}
	json, err := json.Marshal(data)
	if err != nil {
		Log('x', "Could not marshal sync data: %s\n", err.Error())
		return ""
	}

	return string(json)
}

func (ossh *OSSHServer) statsHash() string {
	return StringToSha256(ossh.statsJSON())
}

func (ossh *OSSHServer) loadFingerprints() {
	if FileExists(Conf.PathFingerprints) {
		content, err := os.ReadFile(Conf.PathFingerprints)
		if err != nil {
			Log('x', "Failed to read fingerprints file: %s\n", err.Error())
			return
		}
		fingerprints := strings.Split(string(content), "\n")

		Log('+', "Loading %d fingerprints\n", len(fingerprints))
		for _, fp := range fingerprints {
			ossh.addFingerprint(fp)
		}
	}
}

func (ossh *OSSHServer) loadUsers() {
	if FileExists(Conf.PathUsers) {
		content, err := os.ReadFile(Conf.PathUsers)
		if err != nil {
			Log('x', "Failed to read users file: %s\n", err.Error())
			return
		}
		users := strings.Split(string(content), "\n")

		Log('+', "Loading %d users\n", len(users))
		for _, eu := range users {
			ossh.addUser(eu)
		}
	}
}

func (ossh *OSSHServer) loadPasswords() {
	if FileExists(Conf.PathPasswords) {
		content, err := os.ReadFile(Conf.PathPasswords)
		if err != nil {
			Log('x', "Failed to read passwords file: %s\n", err.Error())
			return
		}
		passwords := strings.Split(string(content), "\n")

		Log('+', "Loading %d passwords\n", len(passwords))
		for _, ep := range passwords {
			ossh.addPassword(ep)
		}
	}
}

func (ossh *OSSHServer) loadHosts() {
	if FileExists(Conf.PathHosts) {
		content, err := os.ReadFile(Conf.PathHosts)
		if err != nil {
			Log('x', "Failed to read hosts file: %s\n", err.Error())
			return
		}
		hosts := strings.Split(string(content), "\n")

		Log('+', "Loading %d hosts\n", len(hosts))
		for _, eh := range hosts {
			ossh.addHost(eh)
		}
	}
}

func (ossh *OSSHServer) saveFingerprints() {
	data := strings.Join(maps.Keys(ossh.Stats.Fingerprints), "\n") + "\n"
	err := os.WriteFile(Conf.PathFingerprints, []byte(data), 0644)
	if err != nil {
		Log('x', "Failed to write fingerprints file: %s\n", err.Error())
	}
}

func (ossh *OSSHServer) saveUsers() {
	data := strings.Join(maps.Keys(ossh.Stats.Users), "\n") + "\n"
	err := os.WriteFile(Conf.PathUsers, []byte(data), 0644)
	if err != nil {
		Log('x', "Failed to write users file: %s\n", err.Error())
	}
}

func (ossh *OSSHServer) savePasswords() {
	data := strings.Join(maps.Keys(ossh.Stats.Passwords), "\n") + "\n"
	err := os.WriteFile(Conf.PathPasswords, []byte(data), 0644)
	if err != nil {
		Log('x', "Failed to write passwords file: %s\n", err.Error())
	}
}

func (ossh *OSSHServer) saveHosts() {
	data := strings.Join(maps.Keys(ossh.Stats.Hosts), "\n") + "\n"
	err := os.WriteFile(Conf.PathHosts, []byte(data), 0644)
	if err != nil {
		Log('x', "Failed to write hosts file: %s\n", err.Error())
	}
}

func (ossh *OSSHServer) saveCapture(stats *FakeShellStats) {
	date := time.Now().String()
	if _, ok := ossh.shells[stats.Host]; ok {
		date = ossh.shells[stats.Host].created.String()
	}
	data := struct {
		Commands string
		Host     string
		User     string
		Date     string
	}{
		Host:     stats.Host,
		User:     stats.User,
		Date:     date,
		Commands: strings.Join(stats.CommandHistory, "\n"),
	}
	resSha1 := StringToSha1(data.Commands)
	f := fmt.Sprintf("%s/ocap-%s-%s.sh", Conf.PathCaptures, stats.Host, resSha1)

	if FileExists(f) {
		return // no need to save, we already have this attack
	}

	ossh.addFingerprint(resSha1)
	res := ParseTemplateToString("command-history", data)
	err := os.WriteFile(f, []byte("\n"+res+"\n\n"), 0744)
	if err == nil {
		Log('✓', "Capture saved: %s\n", colorWrap(f, 214))
	}
}

func (ossh *OSSHServer) hasFingerprint(sha1 string) bool {
	if _, ok := ossh.Stats.Fingerprints[sha1]; !ok {
		return false
	}
	return true
}

func (ossh *OSSHServer) hasUser(usr string) bool {
	if _, ok := ossh.Stats.Users[usr]; !ok {
		return false
	}
	return true
}

func (ossh *OSSHServer) hasPassword(pwd string) bool {
	if _, ok := ossh.Stats.Passwords[pwd]; !ok {
		return false
	}
	return true
}

func (ossh *OSSHServer) hasHost(host string) bool {
	if _, ok := ossh.Stats.Hosts[host]; !ok {
		return false
	}
	return true
}

func (ossh *OSSHServer) getSyncSecrets(host string) (SyncNode, error) {
	for _, node := range Conf.Sync.Nodes {
		if node.Host == host {
			return node, nil
		}
	}
	return SyncNode{}, fmt.Errorf("No sync secrets found for %s", host)
}

func (ossh *OSSHServer) addFingerprint(sha1 string) {
	sha1 = strings.TrimSpace(sha1)
	if sha1 == "" {
		return
	}

	if !ossh.hasFingerprint(sha1) {
		ossh.Stats.Fingerprints[sha1] = 0
	}
	ossh.Stats.Fingerprints[sha1]++
}

func (ossh *OSSHServer) addUser(usr string) {
	usr = strings.TrimSpace(usr)
	if usr == "" {
		return
	}

	if !ossh.hasUser(usr) {
		ossh.Stats.Users[usr] = 0
	}
	ossh.Stats.Users[usr]++
}

func (ossh *OSSHServer) addPassword(pwd string) {
	pwd = strings.TrimSpace(pwd)
	if pwd == "" {
		return
	}

	if !ossh.hasPassword(pwd) {
		ossh.Stats.Passwords[pwd] = 0
	}
	ossh.Stats.Passwords[pwd]++
}

func (ossh *OSSHServer) addHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}

	if !ossh.hasHost(host) {
		ossh.Stats.Hosts[host] = 0
		ossh.Stats.Logins.Attempts[host] = 0
		ossh.Stats.Logins.Failed[host] = 0
		ossh.Stats.Logins.OK[host] = 0
	}
	ossh.Stats.Hosts[host]++
}

func (ossh *OSSHServer) addLoginFailure(usr, pwd, host, reason string) {
	if pwd == "" {
		pwd = "(empty)"
	}
	ossh.addUser(usr)
	ossh.addPassword(pwd)
	ossh.addHost(host)
	ossh.Stats.Logins.Attempts = ossh.incCounter(ossh.Stats.Logins.Attempts, host)
	ossh.Stats.Logins.Failed = ossh.incCounter(ossh.Stats.Logins.Failed, host)
	Log('-', "%s@%s failed to login with password %s: %s. (%d attempts; %d failed; %d success)\n", colorWrap(usr, 193), colorWrap(host, 229), colorWrap(pwd, 157), colorWrap(reason, 208), ossh.Stats.Logins.Attempts[host], ossh.Stats.Logins.Failed[host], ossh.Stats.Logins.OK[host])
}

func (ossh *OSSHServer) addLoginSuccess(usr, pwd, host, reason string) {
	if pwd == "" {
		pwd = "(empty)"
	}
	ossh.addUser(usr)
	ossh.addPassword(pwd)
	ossh.addHost(host)
	ossh.Stats.Logins.Attempts = ossh.incCounter(ossh.Stats.Logins.Attempts, host)
	ossh.Stats.Logins.OK = ossh.incCounter(ossh.Stats.Logins.OK, host)
	Log('+', "%s@%s logged in with password %s: %s. (%d attempts; %d failed; %d success)\n", colorWrap(usr, 193), colorWrap(host, 229), colorWrap(pwd, 157), colorWrap(reason, 121), ossh.Stats.Logins.Attempts[host], ossh.Stats.Logins.Failed[host], ossh.Stats.Logins.OK[host])
}

func (ossh *OSSHServer) incCounter(stat map[string]uint, host string) map[string]uint {
	h := stat[host]
	stat[host] = h + 1
	return stat
}

func (ossh *OSSHServer) sessionHandler(s ssh.Session) {
	fs := NewFakeShell(s)
	host := fs.Host()
	ossh.shells[host] = fs
	stats := fs.Process()

	if !ossh.syncClients[host] {
		ossh.Stats.TimeWasted += int(stats.TimeSpent)

		Log('✓', "%s@%s spent %s running %s command(s)\n",
			colorWrap(fs.User(), 193),
			colorWrap(host, 229),
			colorWrap(time.Duration(stats.TimeSpent*uint(time.Second)).String(), 123),
			colorWrap(fmt.Sprintf("%d", stats.CommandsExecuted), 51),
		)
	}

	ossh.saveUsers()
	ossh.savePasswords()
	ossh.saveHosts()
	ossh.saveFingerprints()

	if !ossh.syncClients[host] {
		ossh.saveCapture(stats)
	}

	delete(ossh.shells, host)
}

func (ossh *OSSHServer) localPortForwardingCallback(ctx ssh.Context, bindHost string, bindPort uint32) bool {
	Log('!', "%s@%s tried to locally forward port %s. Request denied!\n",
		colorWrap(ctx.User(), 193),
		colorWrap(bindHost, 229),
		colorWrap(fmt.Sprintf("%d", bindPort), 51),
	)

	return false
}

func (ossh *OSSHServer) reversePortForwardingCallback(ctx ssh.Context, bindHost string, bindPort uint32) bool {
	Log('!', "%s@%s tried to reverse forward port %s. Request denied!\n",
		colorWrap(ctx.User(), 193),
		colorWrap(bindHost, 229),
		colorWrap(fmt.Sprintf("%d", bindPort), 51),
	)

	return false
}

func (ossh *OSSHServer) ptyCallback(ctx ssh.Context, pty ssh.Pty) bool {
	host := strings.Split(ctx.RemoteAddr().String(), ":")[0]
	if ossh.syncClients[host] {
		return true
	}
	Log('+', "%s@%s started %s PTY session\n",
		colorWrap(ctx.User(), 193),
		colorWrap(host, 229),
		colorWrap(pty.Term, 51),
	)
	return true
}

func (ossh *OSSHServer) sessionRequestCallback(sess ssh.Session, requestType string) bool {
	host := strings.Split(sess.RemoteAddr().String(), ":")[0]
	if ossh.syncClients[host] {
		return true
	}
	Log('+', "%s@%s requested %s session\n",
		colorWrap(sess.User(), 193),
		colorWrap(host, 229),
		colorWrap(requestType, 51),
	)
	return true
}

func (ossh *OSSHServer) connectionFailedCallback(conn net.Conn, err error) {
	if err.Error() != "EOF" {
		host := strings.Split(conn.RemoteAddr().String(), ":")[0]
		if ossh.hasHost(host) {
			if _, ok := ossh.shells[host]; ok {
				Log('!', "%s@%s's connection failed: %s\n",
					colorWrap(ossh.shells[host].stats.User, 193),
					colorWrap(host, 229),
					colorWrap(err.Error(), 208),
				)
				return
			}
		}

		Log('!', "%s's connection failed: %s\n",
			colorWrap(host, 229),
			colorWrap(err.Error(), 208),
		)
	}
}

func (ossh *OSSHServer) authHandler(ctx ssh.Context, pwd string) bool {
	usr := ctx.User()
	host := strings.Split(ctx.RemoteAddr().String(), ":")[0]

	for _, node := range Conf.Sync.Nodes {
		if usr == node.User && pwd == node.Password && node.Host == host {
			// secret credentials hit, let's mark as a sync client
			ossh.syncClients[host] = true
			return true
		}
		ossh.syncClients[host] = false
	}

	if ossh.hasHost(host) {
		ossh.addLoginSuccess(usr, pwd, host, "host is back for more")
		return true // let's see what it wants
	}

	if ossh.hasUser(usr) && ossh.hasPassword(pwd) {
		ossh.addLoginFailure(usr, pwd, host, "host does not have new credentials")
		return false // come back when you have something we don't know yet!
	}

	if ossh.hasUser(usr) {
		ossh.addLoginSuccess(usr, pwd, host, "host got the user name right")
		return true // ok, we'll take it
	}

	if ossh.hasPassword(pwd) {
		ossh.addLoginSuccess(usr, pwd, host, "host got the password right")
		return true // ok, we'll take it
	}

	// ok, the attacker has credentials we don't know yet, let's roll dice.
	if time.Now().Unix()%3 != 0 {
		ossh.addLoginFailure(usr, pwd, host, "host lost a game of dice")
		return false // no luck, big boy, try again
	}

	ossh.addLoginSuccess(usr, pwd, host, "host dodged all obstacles")
	return true
}

func (ossh *OSSHServer) init() {
	ossh.loadHosts()
	ossh.loadUsers()
	ossh.loadPasswords()
	ossh.loadFingerprints()
	ossh.server = &ssh.Server{
		Addr:                          fmt.Sprintf("%s:%d", Conf.Host, Conf.Port),
		Handler:                       ossh.sessionHandler,
		PasswordHandler:               ossh.authHandler,
		IdleTimeout:                   time.Duration(Conf.MaxIdleTimeout) * time.Second,
		ReversePortForwardingCallback: ossh.reversePortForwardingCallback,
		LocalPortForwardingCallback:   ossh.localPortForwardingCallback,
		PtyCallback:                   ossh.ptyCallback,
		ConnectionFailedCallback:      ossh.connectionFailedCallback,
		SessionRequestCallback:        ossh.sessionRequestCallback,
		Version:                       ossh.Version,
	}
}

func (ossh *OSSHServer) Start() {
	Log(' ', "Starting oSSH Server on %v\n", colorWrap(ossh.server.Addr, 229))
	log.Fatal(ossh.server.ListenAndServe())
}

func NewOSSHServer() *OSSHServer {
	ossh := &OSSHServer{
		Version:     Conf.Version,
		server:      nil,
		shells:      map[string]*FakeShell{},
		syncClients: map[string]bool{},
		Stats: struct {
			Logins struct {
				Attempts map[string]uint
				Failed   map[string]uint
				OK       map[string]uint
			}
			Users        map[string]uint
			Passwords    map[string]uint
			Hosts        map[string]uint
			Fingerprints map[string]uint
			TimeWasted   int
		}{
			Logins: struct {
				Attempts map[string]uint
				Failed   map[string]uint
				OK       map[string]uint
			}{
				Attempts: map[string]uint{},
				Failed:   map[string]uint{},
				OK:       map[string]uint{},
			},
			Users:        map[string]uint{},
			Passwords:    map[string]uint{},
			Hosts:        map[string]uint{},
			Fingerprints: map[string]uint{},
			TimeWasted:   0,
		},
	}
	ossh.init()
	go func() {
		for {
			time.Sleep(time.Duration(Conf.Sync.Interval) * time.Minute)
			for _, node := range Conf.Sync.Nodes {
				_ = executeSSHCommand(node.Host, node.Port, node.User, node.Password, "check")
			}
		}
	}()
	return ossh
}
