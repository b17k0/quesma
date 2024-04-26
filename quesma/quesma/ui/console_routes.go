package ui

import (
	"embed"
	"encoding/json"
	"github.com/gorilla/mux"
	"mitmproxy/quesma/logger"
	"mitmproxy/quesma/stats"
	"net/http"
)

const (
	managementInternalPath = "/_quesma"
	healthPath             = managementInternalPath + "/health"
	bypassPath             = managementInternalPath + "/bypass"
)

//go:embed asset/*
var uiFs embed.FS

func (qmc *QuesmaManagementConsole) createRouting() *mux.Router {
	router := mux.NewRouter()

	router.Use(panicRecovery)

	router.HandleFunc(healthPath, qmc.checkHealth)

	router.HandleFunc(bypassPath, bypassSwitch).Methods("POST")

	router.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)

	router.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateDashboard()
		_, _ = writer.Write(buf)
	})

	// /dashboard is referenced in docs and should redirect to /
	router.HandleFunc("/dashboard", func(writer http.ResponseWriter, req *http.Request) {
		http.Redirect(writer, req, "/", http.StatusSeeOther)
	})

	router.HandleFunc("/live", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateLiveTail()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/schema/reload", func(writer http.ResponseWriter, req *http.Request) {
		qmc.logManager.ReloadTables()
		buf := qmc.generateSchema()
		_, _ = writer.Write(buf)
	}).Methods("POST")

	router.HandleFunc("/schema", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateSchema()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/telemetry", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generatePhoneHome()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/routing-statistics", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateRouterStatisticsLiveTail()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/ingest-statistics", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateStatisticsLiveTail()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/statistics-json", func(writer http.ResponseWriter, req *http.Request) {
		jsonBody, err := json.Marshal(stats.GlobalStatistics)
		if err != nil {
			logger.Error().Msgf("Error marshalling statistics: %v", err)
			writer.WriteHeader(500)
			return
		}
		_, _ = writer.Write(jsonBody)
		writer.WriteHeader(200)
	})

	router.HandleFunc("/panel/routing-statistics", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateRouterStatistics()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/panel/statistics", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateStatistics()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/panel/queries", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateQueries()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/panel/dashboard", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateDashboardPanel()
		_, _ = writer.Write(buf)
	})

	router.HandleFunc("/panel/dashboard-traffic", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateDashboardTrafficPanel()
		_, _ = writer.Write(buf)
	})

	router.PathPrefix("/request-Id/{requestId}").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		buf := qmc.generateReportForRequestId(vars["requestId"])
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/log/{requestId}").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		buf := qmc.generateLogForRequestId(vars["requestId"])
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/error/{reason}").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		buf := qmc.generateErrorForReason(vars["reason"])
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/requests-by-str/{queryString}").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		buf := qmc.generateReportForRequestsWithStr(vars["queryString"])
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/requests-with-error/").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		buf := qmc.generateReportForRequestsWithError()
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/requests-with-warning/").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		buf := qmc.generateReportForRequestsWithWarning()
		_, _ = writer.Write(buf)
	})
	router.PathPrefix("/request-Id").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		// redirect to /
		http.Redirect(writer, r, "/", http.StatusSeeOther)
	})
	router.PathPrefix("/requests-by-str").HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		// redirect to /
		http.Redirect(writer, r, "/", http.StatusSeeOther)
	})
	router.HandleFunc("/queries", func(writer http.ResponseWriter, req *http.Request) {
		buf := qmc.generateQueries()
		_, _ = writer.Write(buf)
	})

	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(uiFs))))
	return router
}
