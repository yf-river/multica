package lark

import (
	"context"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type EndpointFetcherFunc func(context.Context, InstallationCredentials) (WSEndpoint, error)

func (f EndpointFetcherFunc) Endpoint(ctx context.Context, creds InstallationCredentials) (WSEndpoint, error) {
	return f(ctx, creds)
}

type FrameDecoderFunc func([]byte, db.LarkInstallation) (InboundMessage, bool, error)

func (f FrameDecoderFunc) Decode(payload []byte, inst db.LarkInstallation) (InboundMessage, bool, error) {
	return f(payload, inst)
}
