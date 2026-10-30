package loadchart_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLoadchart(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Loadchart Suite")
}
