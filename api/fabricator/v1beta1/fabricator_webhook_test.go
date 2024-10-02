// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Fabricator Webhook", func() {
	Context("When creating Fabricator under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {
			// TODO(user): Add your logic here
		})
	})

	Context("When creating Fabricator under Validating Webhook", func() {
		It("Should deny if a required field is empty", func() {
			// TODO(user): Add your logic here
		})

		It("Should admit if all required fields are provided", func() {
			// TODO(user): Add your logic here
		})
	})
})
