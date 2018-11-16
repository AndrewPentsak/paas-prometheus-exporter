package exporter

import (
	"log"
	"net/url"

	"time"

	"github.com/alphagov/paas-prometheus-exporter/events"
	"github.com/cloudfoundry-community/go-cfclient"

	"github.com/prometheus/client_golang/prometheus"
)

//go:generate counterfeiter -o mocks/cfclient.go . CFClient
type CFClient interface {
	ListAppsByQuery(url.Values) ([]cfclient.App, error)
}

//go:generate counterfeiter -o mocks/watcher_manager.go . WatcherManager
type WatcherManager interface {
	AddWatcher(cfclient.App, prometheus.Registerer)
	DeleteWatcher(appGuid string)
	UpdateAppInstances(appGuid string, instances int)
}

type ConcreteWatcherManager struct {
	config         *cfclient.Config
	watchers       map[string]*events.AppWatcher
}

func NewWatcherManager(config *cfclient.Config) WatcherManager {
	return &ConcreteWatcherManager{
		config:   config,
		watchers: make(map[string]*events.AppWatcher),
	}
}

func (wm *ConcreteWatcherManager) AddWatcher(app cfclient.App, registry prometheus.Registerer) {
	var provider events.AppStreamProvider = &events.DopplerAppStreamProvider{
		Config: wm.config,
	}
	wm.watchers[app.Guid] =	events.NewAppWatcher(app.Guid, app.Instances, registry, provider)
}

func (wm *ConcreteWatcherManager) DeleteWatcher(appGuid string) {
	wm.watchers[appGuid].Close()
	delete(wm.watchers, appGuid)
}

func (wm *ConcreteWatcherManager) UpdateAppInstances(appGuid string, instances int) {
	wm.watchers[appGuid].UpdateAppInstances(instances)
}

type PaasExporter struct {
	cf             CFClient
	watcherManager WatcherManager
	nameByGuid     map[string]string
}

func New(cf CFClient, wc WatcherManager) *PaasExporter {
	return &PaasExporter{
		cf:             cf,
		watcherManager: wc,
		nameByGuid:     make(map[string]string),
	}
}

func (e *PaasExporter) createNewWatcher(app cfclient.App) {
	e.nameByGuid[app.Guid] = app.Name
	e.watcherManager.AddWatcher(app, prometheus.WrapRegistererWith(
		prometheus.Labels{"guid": app.Guid, "app": app.Name},
		prometheus.DefaultRegisterer,
	))
}

func (e *PaasExporter) checkForNewApps() error {
	apps, err := e.cf.ListAppsByQuery(url.Values{})
	if err != nil {
		return err
	}

	running := map[string]bool{}

	for _, app := range apps {
		// Do we need to check they're running or does the API return all of them?
		// need to check app.State is "STARTED"
		running[app.Guid] = true

		if _, ok := e.nameByGuid[app.Guid]; ok {
			if e.nameByGuid[app.Guid] != app.Name {
				// Name changed, stop and restart
				e.watcherManager.DeleteWatcher(app.Guid)
				e.createNewWatcher(app)
			} else {
				// notify watcher that instances may have changed
				e.watcherManager.UpdateAppInstances(app.Guid, app.Instances)
			}
		} else {
			// new app
			e.createNewWatcher(app)
		}
	}

	for appGuid, _ := range e.nameByGuid {
		if ok := running[appGuid]; !ok {
			e.watcherManager.DeleteWatcher(appGuid)
		}
	}
	return nil
}

func (e *PaasExporter) Start(updateFrequency time.Duration) {
	for {
		log.Println("checking for new apps")
		err := e.checkForNewApps()
		if err != nil {
			log.Fatal(err)
		}

		time.Sleep(updateFrequency)
	}
}
