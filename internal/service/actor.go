package service

import "context"

// Actor is the person performing an action (owner or operator); used to sign replies sent from the
// Mini App in the operators' topic. The user never sees it.
type Actor struct {
	ID   int64
	Name string
}

type actorKey struct{}

// WithActor attaches the acting person to ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the acting person, zero if unknown.
func ActorFrom(ctx context.Context) Actor {
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}
