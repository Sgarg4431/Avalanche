package controller

import (
	ametrics "github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/avalanchego/utils/wrappers"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rafael-abuawad/samplevm/consts"
)

type metrics struct {
	createAsset prometheus.Counter
	mintAsset   prometheus.Counter
	burnAsset   prometheus.Counter
	modifyAsset prometheus.Counter
	transfer    prometheus.Counter
	importAsset prometheus.Counter
	exportAsset prometheus.Counter
}

func newMetrics(gatherer ametrics.MultiGatherer) (*metrics, error) {
	m := &metrics{
		createAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "create_asset",
			Help:      "number of create asset actions",
		}),
		mintAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "mint_asset",
			Help:      "number of mint asset actions",
		}),
		burnAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "burn_asset",
			Help:      "number of burn asset actions",
		}),
		modifyAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "modify_asset",
			Help:      "number of modify asset actions",
		}),
		transfer: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "transfer",
			Help:      "number of transfer actions",
		}),
		importAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "import_asset",
			Help:      "number of import asset actions",
		}),
		exportAsset: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "actions",
			Name:      "export_asset",
			Help:      "number of export asset actions",
		}),
	}
	r := prometheus.NewRegistry()
	errs := wrappers.Errs{}
	errs.Add(
		r.Register(m.createAsset),
		r.Register(m.mintAsset),
		r.Register(m.burnAsset),
		r.Register(m.modifyAsset),
		r.Register(m.transfer),
		r.Register(m.importAsset),
		r.Register(m.exportAsset),
		gatherer.Register(consts.Name, r),
	)
	return m, errs.Err
}
