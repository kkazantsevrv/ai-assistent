// Package netfix чинит сеть в окружениях без /etc/resolv.conf и системных
// сертификатов — в первую очередь в Termux на Android, где чистый Go-резолвер
// и поиск корневых сертификатов иначе не находят нужных файлов.
package netfix

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const termuxPrefix = "/data/data/com.termux/files/usr"

// Apply настраивает DNS и сертификаты, если это нужно. dnsServers — явный
// список серверов вида "1.1.1.1:53" (из конфига), может быть пустым.
func Apply(dnsServers []string) {
	fixCerts()
	fixDNS(dnsServers)
}

func prefix() string {
	if p := os.Getenv("PREFIX"); p != "" {
		return p
	}
	return termuxPrefix
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fixCerts() {
	if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return
	}
	// Если стандартные Linux-пути есть — Go сам их найдёт.
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if exists(p) {
			return
		}
	}
	for _, p := range []string{
		filepath.Join(prefix(), "etc/tls/cert.pem"),
		termuxPrefix + "/etc/tls/cert.pem",
	} {
		if exists(p) {
			os.Setenv("SSL_CERT_FILE", p)
			log.Printf("netfix: использую сертификаты %s", p)
			return
		}
	}
	if exists("/system/etc/security/cacerts") {
		os.Setenv("SSL_CERT_DIR", "/system/etc/security/cacerts")
		log.Printf("netfix: использую сертификаты Android /system/etc/security/cacerts")
	}
}

func fixDNS(servers []string) {
	if len(servers) == 0 {
		if exists("/etc/resolv.conf") {
			return // обычный Linux — ничего не трогаем
		}
		servers = readResolvConf(filepath.Join(prefix(), "etc/resolv.conf"))
		if len(servers) == 0 {
			servers = []string{"8.8.8.8:53", "1.1.1.1:53"}
		}
	}

	log.Printf("netfix: DNS-серверы %s", strings.Join(servers, ", "))
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			var errs []error
			for _, s := range servers {
				conn, err := d.DialContext(ctx, network, s)
				if err == nil {
					return conn, nil
				}
				errs = append(errs, err)
			}
			return nil, errors.Join(errs...)
		},
	}
}

func readResolvConf(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, net.JoinHostPort(fields[1], "53"))
		}
	}
	return out
}
