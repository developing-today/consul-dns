package dns

import (
	"github.com/miekg/dns"
	"github.com/ennetech/consul-dns/pkg/logger"
	"github.com/ennetech/consul-dns/pkg/config"
	"github.com/ennetech/consul-dns/pkg/zone"
	"github.com/ennetech/consul-dns/pkg/dns/operations"
	"strings"
	"github.com/ennetech/consul-dns/pkg/dns/request"
)

// Repository used for storage
var repository zone.Repository
var conf config.ConsulDnsConfig

func Init(c config.ConsulDnsConfig, repo zone.Repository) {
	repository = repo
	conf = c
	startServers(conf.SystemConfig.DnsPort, handle)
}

// Simple metric
var requestCounter int

func handle(w dns.ResponseWriter, r *dns.Msg) {
	requestCounter++

	// Hold the records until we process all the pipeline
	var responseRecords []dns.RR

	// Mantain the same zone across same request
	var z zone.Zone

	switch r.Opcode {
	case dns.OpcodeQuery:
		for _, q := range r.Question {
			if strings.HasSuffix(q.Name, ".consul.") {
				rf, err := request.Forward(conf.ConsulConfig.DnsAddress, q.Name, q.Qtype)
				if err == nil {
					responseRecords = append(responseRecords, rf...)
				}
			} else {
				err := checkZone(&z, q.Name)
				if err != nil {
					sendNxDomain(w, r)
					return
				}

				if strings.Contains(q.Name, ".service.") || strings.Contains(q.Name, ".node.") {
					rr, err := operations.HandleMasquerade(q.Name, q.Qtype, z.Origin(), conf.ConsulConfig.DnsAddress)
					if err == nil {
						responseRecords = append(responseRecords, rr...)
					}
				} else {
					r := operations.HandleQuery(q, z)
					responseRecords = append(responseRecords, r...)
				}
			}
		}
	case dns.OpcodeUpdate:
		tsig := r.IsTsig()
		if (tsig != nil) {
			logger.Info("UPDATE HAS TSIG ")
			secret := conf.SystemConfig.TsigKey
			pack, _ := r.Pack()
			err := dns.TsigVerify(pack, secret, "", false)
			if (err != nil) {
				logger.Error("TSIG VERIFICATION FAILED " + err.Error())
			} else {
				logger.Error("TSIG VERIFICATION SUCCEDEED")
			}
			responseRecords = append(responseRecords, &dns.TXT{
				Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0},
				Txt: []string{"- TSIG PRESENT -"},
			})
		}
		for _, ns := range r.Ns {
			err := checkZone(&z, ns.Header().Name)
			if err != nil {
				sendNxDomain(w, r)
				return
			}
			err = operations.HandleUpdate(ns, z)
			if err != nil {
				sendRefused(w, r)
				return
			}
		}
	default:
		sendNotImplemented(w, r)
		return
	}

	sendSuccess(w, r, responseRecords)
}