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
	"path"
	"strings"
)

/* commandline flags */
type cmdlineArgs struct {
	Config   string // Path to configuration file
	Host     string // Host IP or name to bind to
	Port     int    // Port to bind to
	Maildirs string // Path to top level of the user Maildirs
}

/* commandline defaults */
var cmdline = cmdlineArgs{
	Config:   "letterbox.toml",
	Host:     "",
	Port:     25,
	Maildirs: "/var/spool/maildirs",
}

/* parseArgs handles parsing the cmdline args and setting values in the global cmdline struct */
func parseArgs() {
	flag.StringVar(&cmdline.Config, "config", cmdline.Config, "Path to configutation file")
	flag.StringVar(&cmdline.Host, "host", cmdline.Host, "Host IP or name to bind to")
	flag.IntVar(&cmdline.Port, "port", cmdline.Port, "Port to bind to")
	flag.StringVar(&cmdline.Maildirs, "maildirs", cmdline.Maildirs, "Path to the top level of the user Maildirs")

	flag.Parse()
}

type letterboxConfig struct {
	Hosts  []string `toml:"hosts"`
	Emails []string `toml:"emails"`
}

var cfg letterboxConfig
var allowedHosts []net.IP
var allowedNetworks []*net.IPNet

// reads a TOML configuration file and returns a slice of settings
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

type env struct {
	rcpts      []smtpd.MailAddress
	destDirs   []*maildir.Dir
	deliveries []*maildir.Delivery
	tmpfile    string
}

func (e *env) AddRecipient(rcpt smtpd.MailAddress) error {
	if strings.HasPrefix(rcpt.Email(), "bad@") {
		return errors.New("we don't send email to bad@")
	}
	// Check the recipients against the whitelist. Only append ones that pass
	e.rcpts = append(e.rcpts, rcpt)
	return nil
}

func (e *env) BeginData() error {
	if len(e.rcpts) == 0 {
		return smtpd.SMTPError("554 5.5.1 Error: no valid recipients")
	}

	for _, rcpt := range e.rcpts {
		if !strings.Contains(rcpt.Email(), "@") {
			log.Printf("Skipping recipient: %s", rcpt)
			continue
		}
		// Eliminate anything that looks like a path
		user := path.Base(path.Clean(strings.Split(rcpt.Email(), "@")[0]))

		// TODO filter recipients based on a whitelist

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

// The server really should call this with error status from outside
func (e *env) Close() error {
	for _, delivery := range e.deliveries {
		err := delivery.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func onNewConnection(c smtpd.Connection) error {
	client, _, err := net.SplitHostPort(c.Addr().String())
	if err != nil {
		log.Printf("Problem parsing client address %s: %s", c.Addr().String(), err)
		return errors.New("Problem parsing client address")
	}
	clientIP := net.ParseIP(client)
	log.Printf("Connection from %s\n", clientIP.String())
	for _, h := range allowedHosts {
		if h.Equal(clientIP) {
			log.Printf("Connection from %s allowed by hosts\n", clientIP.String())
			return nil
		}
	}

	for _, n := range allowedNetworks {
		if n.Contains(clientIP) {
			log.Printf("Connection from %s allowed by network\n", clientIP.String())
			return nil
		}
	}

	log.Printf("Connection from %s rejected\n", clientIP.String())
	return errors.New("Client IP not allowed")
}

func onNewMail(c smtpd.Connection, from smtpd.MailAddress) (smtpd.Envelope, error) {
	log.Printf("letterbox: new mail from %q", from)
	return &env{}, nil
}

func main() {
	parseArgs()
	cfg, err := readConfig(cmdline.Config)
	if err != nil {
		log.Fatalf("Error reading config file %s: %s\n", cmdline.Config, err)
	}
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
	fmt.Printf("letterbox: %s:%d\n", cmdline.Host, cmdline.Port)
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
