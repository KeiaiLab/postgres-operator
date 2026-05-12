/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package election

import "context"

// Follower 는 standalone replica cluster 전용 election 구현이다. 항상 Follower
// 상태를 유지하고 leadership callback 을 호출하지 않는다. 외부 primary 를 따라가는
// continuous recovery pod 가 자체 promote 되는 것을 차단한다.
type Follower struct {
	*statusHolder
	identity string
}

// NewFollower 는 영구 follower election 을 만든다.
func NewFollower(identity string, _ Callbacks) *Follower {
	return &Follower{
		statusHolder: newStatusHolder(StatusFollower),
		identity:     identity,
	}
}

// Identity 는 identity 를 반환한다.
func (f *Follower) Identity() string { return f.identity }

// Run 은 ctx 종료까지 block 하며 leadership callback 을 호출하지 않는다.
func (f *Follower) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

var _ Election = (*Follower)(nil)
