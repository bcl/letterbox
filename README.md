# letterbox - SMTP to Maildir delivery agent

This is a simple Go program that accepts SMTP connections and delivers mail to
a per-user maildir directory. I use it to gather reports from various services
on my LAN without needing to setup postfix or some other more complex MTA.

    Usage of letterbox:
      -config string
            Path to configutation file (default "letterbox.toml")
      -host string
            Host IP or name to bind to
      -maildirs string
            Path to the top level of the user Maildirs (default "/var/spool/maildirs")
      -port int
            Port to bind to (default 25)

The configuration file is written using
[TOML](https://github.com/toml-lang/toml). You must specify at least one
host/network and one email otherwise delivery will fail. For example:

    hosts = ["192.168.1.0/24", "127.0.0.1", "logger.mydomain.com"]
    emails = ["root@mydomain.com", "user@another.com"]

If the connection is not from an allowed host the connection will be refused.
Destination emails must be listed in the `emails` list. The user portion of the
email will be used to create a new maildir under the `-maildirs` path. For
example, sending an email to user@another.com will create a new maildir at
`/var/spool/maildirs/user`.

# WARNING

This code is not meant to be run on the open network. Make sure it is protected behind a firewall,
and is running as an un-privileged user. *Never* run it as root.
