/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package kube holds small shared controller helpers.
package kube

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Upsert creates desired when absent, or applies mutate to the
// existing object and updates it. The caller supplies the desired
// object with ObjectMeta and (for creates) the full spec; mutate
// copies the mutable fields onto the existing object for updates.
func Upsert(ctx context.Context, c client.Client, desired client.Object, mutate func(existing client.Object)) error {
	existing := desired.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return c.Create(ctx, desired)
		}
		return err
	}
	mutate(existing)
	return c.Update(ctx, existing)
}
