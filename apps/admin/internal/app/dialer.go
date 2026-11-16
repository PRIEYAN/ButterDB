package app

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	connectvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

type APIDialer struct{}

func NewDialer() *APIDialer {
	return &APIDialer{}
}

func (*APIDialer) Probe(ctx context.Context, baseURL string) (adminv1.CertificateInfo, error) {
	return adminv1.ProbeCertificate(ctx, baseURL)
}

func (*APIDialer) Dial(cfg adminv1.ClientConfig) (connectvm.ConnectClient, error) {
	client, err := adminv1.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return client, nil
}
