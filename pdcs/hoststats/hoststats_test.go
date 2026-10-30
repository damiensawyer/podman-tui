//go:build linux

package hoststats

import (
	"errors"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHoststats(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hoststats Suite")
}

var _ = Describe("parseProcStat", func() {
	It("extracts total and busy jiffies from a cpu line", func() {
		// user nice system idle iowait irq softirq steal
		data := []byte("cpu  100 0 200 600 100 50 50 0 0 0\n" +
			"cpu0 100 0 200 600 100 50 50 0 0 0\n")

		total, busy, ok := parseProcStat(data)

		Expect(ok).To(BeTrue())
		// total = 100+0+200+600+100+50+50 = 1100
		Expect(total).To(Equal(uint64(1100)))
		// busy = total - (idle 600 + iowait 100) = 400
		Expect(busy).To(Equal(uint64(400)))
	})

	It("rejects truncated lines", func() {
		_, _, ok := parseProcStat([]byte("cpu 1 2 3\n"))
		Expect(ok).To(BeFalse())
	})

	It("rejects non numeric fields", func() {
		_, _, ok := parseProcStat([]byte("cpu 1 2 3 x 5 6 7\n"))
		Expect(ok).To(BeFalse())
	})

	It("reports failure without a cpu line", func() {
		_, _, ok := parseProcStat([]byte("intr 123\n"))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("parseCgroupUsage", func() {
	It("extracts the usage_usec counter", func() {
		data := []byte("usage_usec 123456\n" +
			"user_usec 100000\n" +
			"system_usec 23456\n" +
			"nr_periods 0\n")

		usage, ok := parseCgroupUsage(data)

		Expect(ok).To(BeTrue())
		Expect(usage).To(Equal(uint64(123456)))
	})

	It("fails without a usage_usec line", func() {
		_, ok := parseCgroupUsage([]byte("nr_periods 4\n"))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("CPUPercentFromDeltas", func() {
	It("converts two snapshots into a percentage", func() {
		// half of the delta was busy
		percent := CPUPercentFromDeltas(1000, 100, 2000, 600)
		Expect(percent).To(BeNumerically("~", 50.0, 0.001))
	})

	It("returns zero when the total did not advance", func() {
		Expect(CPUPercentFromDeltas(1000, 100, 1000, 100)).To(Equal(0.0))
	})

	It("clamps negative deltas to zero", func() {
		Expect(CPUPercentFromDeltas(2000, 100, 1000, 100)).To(Equal(0.0))
		Expect(CPUPercentFromDeltas(1000, 500, 2000, 100)).To(Equal(0.0))
	})

	It("clamps to 100 percent", func() {
		// busy grew by the whole total delta
		Expect(CPUPercentFromDeltas(1000, 100, 1100, 200)).To(Equal(100.0))
	})
})

var _ = Describe("Reader", func() {
	It("returns ErrUnavailable on non linux platforms", func() {
		if runtime.GOOS == "linux" {
			Skip("linux reads /proc/stat instead")
		}

		reader := NewReader()

		value, err := reader.Sample()
		Expect(value).To(Equal(0.0))
		Expect(errors.Is(err, ErrUnavailable)).To(BeTrue())
	})

	It("produces samples on linux", func() {
		if runtime.GOOS != "linux" {
			Skip("non linux targets report ErrUnavailable")
		}

		reader := NewReader()

		first, err := reader.Sample()
		Expect(err).ToNot(HaveOccurred())

		// first call primes the baseline
		Expect(first).To(Equal(0.0))

		second, err := reader.Sample()
		Expect(err).ToNot(HaveOccurred())
		Expect(second).To(BeNumerically(">=", 0))
		Expect(second).To(BeNumerically("<=", 100))
	})
})
