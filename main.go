// letterbox - SMTP to Maildir delivery agent
/*
Copyright (c) 2019, Brian C. Lane <bcl@brianlane.com>
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

	* Redistributions of source code must retain the above copyright notice,
	  this list of conditions and the following disclaimer.
	* Redistributions in binary form must reproduce the above copyright notice,
	  this list of conditions and the following disclaimer in the documentation
	  and/or other materials provided with the distribution.
	* Neither the name of the <ORGANIZATION> nor the names of its contributors
	  may be used to endorse or promote products derived from this software without
	  specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
*/
package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/bradfitz/go-smtpd/smtpd"
	"github.com/luksen/maildir"
	"log"
	"net"
	"os"
	"path"
	"strings"
)

/* commandline flags */
type cmdlineArgs struct {
	Config   string // Path to configuration file
	Host     string // Host IP or name to bind to
	Port     int    // Port to bind to
	Maildirs string // Path to top level of the user Maildirs
	Logfile  string // Path to logfile
	Debug    bool   // Log debugging information
}

/* commandline defaults */
var cmdline = cmdlineArgs{
	Config:   "letterbox.toml",
	Host:     "",
	Port:     2525,
	Maildirs: "/var/spool/maildirs",
	Logfile:  "",
	Debug:    false,
}

/* parseArgs handles parsing the cmdline args and setting values in the global cmdline struct */
func parseArgs() {
	flag.StringVar(&cmdline.Config, "config", cmdline.Config, "Path to configutation file")
	flag.StringVar(&cmdline.Host, "host", cmdline.Host, "Host IP or name to bind to")
	flag.IntVar(&cmdline.Port, "port", cmdline.Port, "Port to bind to")
	flag.StringVar(&cmdline.Maildirs, "maildirs", cmdline.Maildirs, "Path to the top level of the user Maildirs")
	flag.StringVar(&cmdline.Logfile, "log", cmdline.Logfile, "Path to logfile")
	flag.BoolVar(&cmdline.Debug, "debug", cmdline.Debug, "Log debugging information")

	flag.Parse()
}

// Only log if -debug has been passed to the program
func logDebugf(format string, v ...interface{}) {
	if cmdline.Debug {
		log.Printf(format, v...)
	}
}

type letterboxConfig struct {
	Hosts  []string `toml:"hosts"`
	Emails []string `toml:"emails"`
}

var cfg letterboxConfig
var allowedHosts []net.IP
var allowedNetworks []*net.IPNet

// readConfig reads a TOML configuration file and returns a slice of settings
/*
   Example TOML file:

   hosts = ["192.168.101.0/24", "fozzy.brianlane.com", "192.168.103.15"]
   emails = ["user@domain.com", "root@domain.com"]
*/
func readConfig(filename string) (letterboxConfig, error) {
	var config letterboxConfig
	if _, err := toml.DecodeFile(filename, &config); err != nil {
		return config, err
	}
	return config, nil
}

// parseHosts fills the global allowedHosts and allowedNetworks from the cfg.Hosts list
func parseHosts() {
	// Convert the hosts entries into IP and IPNet
	for _, h := range cfg.Hosts {
		// Does it look like a CIDR?
		_, ipv4Net, err := net.ParseCIDR(h)
		if err == nil {
			allowedNetworks = append(allowedNetworks, ipv4Net)
			continue
		}

		// Does it look like an IP?
		ip := net.ParseIP(h)
		if ip != nil {
			allowedHosts = append(allowedHosts, ip)
			continue
		}

		// Does it look like a hostname?
		ips, err := net.LookupIP(h)
		if err == nil {
			for _, ip := range ips {
				allowedHosts = append(allowedHosts, ip)
			}
		}
	}
}

// smtpd.Envelope interface, with some extra data for letterbox delivery
type env struct {
	rcpts      []smtpd.MailAddress
	destDirs   []*maildir.Dir
	deliveries []*maildir.Delivery
	tmpfile    string
}

// AddRecipient is called when RCPT TO is received
// It checks the email against the whitelist and rejects it if it is not an exact match
func (e *env) AddRecipient(rcpt smtpd.MailAddress) error {
	// Match the recipient against the email whitelist
	for _, user := range cfg.Emails {
		if rcpt.Email() == user {
			e.rcpts = append(e.rcpts, rcpt)
			return nil
		}
	}
	return errors.New("Recipient not in whitelist")
}

// BeginData is called when DATA is received
// It sanitizes the revipient email and creates any missing maildirs
func (e *env) BeginData() error {
	if len(e.rcpts) == 0 {
		return smtpd.SMTPError("554 5.5.1 Error: no valid recipients")
	}

	for _, rcpt := range e.rcpts {
		if !strings.Contains(rcpt.Email(), "@") {
			logDebugf("Skipping recipient: %s", rcpt)
			continue
		}
		// Eliminate anything that looks like a path
		user := path.Base(path.Clean(strings.Split(rcpt.Email(), "@")[0]))

		// TODO reroute mail based on /etc/aliases

		// Add a new maildir for each recipient
		userDir := maildir.Dir(path.Join(cmdline.Maildirs, user))
		if err := userDir.Create(); err != nil {
			log.Printf("Error creating maildir for %s: %s", user, err)
			return smtpd.SMTPError("450 Error: maildir unavailable")
		}
		e.destDirs = append(e.destDirs, &userDir)
		delivery, err := userDir.NewDelivery()
		if err != nil {
			log.Printf("Error creating delivery for %s: %s", user, err)
			return smtpd.SMTPError("450 Error: maildir unavailable")
		}
		e.deliveries = append(e.deliveries, delivery)
	}
	if len(e.deliveries) == 0 {
		return smtpd.SMTPError("554 5.5.1 Error: no valid recipients")
	}

	return nil
}

// Write is called for each line of the email
// It supports writing to multiple recipients at the same time.
func (e *env) Write(line []byte) error {
	for _, delivery := range e.deliveries {
		_, err := delivery.Write(line)
		if err != nil {
			// Delivery failed, need to close all the deliveries
			e.Close()
			return err
		}
	}
	return nil
}

// Close is called when the connection is closed
// The server really should call this with error status from outside
// we have no way to know if this is in response to an error or not.
func (e *env) Close() error {
	for _, delivery := range e.deliveries {
		err := delivery.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// onNewConnection is called when a client connects to letterbox
// It checks the client IP against the allowedHosts and allowedNetwork lists,
// rejecting the connection if it doesn't match.
func onNewConnection(c smtpd.Connection) error {
	client, _, err := net.SplitHostPort(c.Addr().String())
	if err != nil {
		log.Printf("Problem parsing client address %s: %s", c.Addr().String(), err)
		return errors.New("Problem parsing client address")
	}
	clientIP := net.ParseIP(client)
	logDebugf("Connection from %s\n", clientIP.String())
	for _, h := range allowedHosts {
		if h.Equal(clientIP) {
			logDebugf("Connection from %s allowed by hosts\n", clientIP.String())
			return nil
		}
	}

	for _, n := range allowedNetworks {
		if n.Contains(clientIP) {
			logDebugf("Connection from %s allowed by network\n", clientIP.String())
			return nil
		}
	}

	logDebugf("Connection from %s rejected\n", clientIP.String())
	return errors.New("Client IP not allowed")
}

// onNewMail is called when a new connection is allowed
// it creates a new envelope struct which is used to hold the information about
// the recipients.
func onNewMail(c smtpd.Connection, from smtpd.MailAddress) (smtpd.Envelope, error) {
	logDebugf("letterbox: new mail from %q", from)
	return &env{}, nil
}

func main() {
	parseArgs()

	// Setup logging to a file if selected
	if len(cmdline.Logfile) > 0 {
		f, err := os.OpenFile(cmdline.Logfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			log.Fatalf("Error opening logfile: %s", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	var err error
	cfg, err = readConfig(cmdline.Config)
	if err != nil {
		log.Fatalf("Error reading config file %s: %s\n", cmdline.Config, err)
	}
	parseHosts()
	log.Printf("letterbox: %s:%d", cmdline.Host, cmdline.Port)
	log.Println("Allowed Hosts")
	for _, h := range allowedHosts {
		log.Printf("    %s\n", h.String())
	}
	log.Println("Allowed Networks")
	for _, n := range allowedNetworks {
		log.Printf("    %s\n", n.String())
	}

	s := &smtpd.Server{
		Addr:            fmt.Sprintf("%s:%d", cmdline.Host, cmdline.Port),
		OnNewConnection: onNewConnection,
		OnNewMail:       onNewMail,
	}
	err = s.ListenAndServe()
	if err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
