package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-103/internal/incident"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/repeater"
)

func (s *Server) getLandingHealth(writer http.ResponseWriter, request *http.Request) {
	if chi.URLParam(request, "id") != s.services.Station.Snapshot().ID {
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: "landing station not found"})
		return
	}
	writeJSON(writer, http.StatusOK, s.services.Health.Evaluate(s.services.Station, s.services.Clock.Now()))
}

func (s *Server) rampRepeater(writer http.ResponseWriter, request *http.Request) {
	target, err := s.services.Repeater(chi.URLParam(request, "id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	var input RampRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := s.services.Ramps.ApplyStep(request.Context(), target, repeater.RampStep{Number: input.Step, Voltage: input.Voltage}, input.SamplesAfter)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"repeater": result, "commands": s.services.Actuator.Commands()})
}

func (s *Server) resetRepeater(writer http.ResponseWriter, request *http.Request) {
	var input ResetRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.services.RepeaterHealth.Reset(chi.URLParam(request, "id"), input.Generation); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	target, _ := s.services.Repeater(chi.URLParam(request, "id"))
	writeJSON(writer, http.StatusOK, target.Snapshot())
}

func (s *Server) admitAlarm(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	var input AlarmRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	alarm := incident.Alarm{ID: s.services.IDs.New("alarm"), SpanID: spanID, Kind: input.Kind, ObservedAt: s.services.Clock.Now()}
	if !s.services.Admission.Accept(alarm) {
		writeJSON(writer, http.StatusAccepted, map[string]any{"admitted": false, "alarm": alarm})
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"admitted": true, "alarm": alarm})
}

func (s *Server) listAlarms(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	writeJSON(writer, http.StatusOK, map[string]any{
		"items":      s.services.Admission.Accepted(spanID),
		"suppressed": s.services.Admission.Suppressed(spanID),
	})
}

func (s *Server) startWetPlantTest(writer http.ResponseWriter, request *http.Request) {
	snapshot := s.services.StartWetPlantTest(request.Context(), chi.URLParam(request, "id"))
	writeJSON(writer, http.StatusCreated, snapshot)
}

func (s *Server) finishWetPlantTest(writer http.ResponseWriter, request *http.Request) {
	var input TestRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	snapshot, err := s.services.FinishWetPlantTest(chi.URLParam(request, "id"), input.Action)
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (s *Server) getIncident(writer http.ResponseWriter, request *http.Request) {
	current := s.services.Incident(chi.URLParam(request, "incidentID"), chi.URLParam(request, "id"))
	writeJSON(writer, http.StatusOK, current.Snapshot())
}

func (s *Server) updateIncident(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	incidentID := chi.URLParam(request, "incidentID")
	var input IncidentRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	current := s.services.Incident(incidentID, spanID)
	now := s.services.Clock.Now()
	var snapshot incident.Snapshot
	switch input.Action {
	case "local-complete":
		snapshot = s.services.Classifier.LocalComplete(current, input.RequiresPeer, now)
	case "peer-evidence":
		pair := s.services.Correlator.Add(otdr.Evidence{
			IncidentID: incidentID, StationID: input.StationID, TraceID: input.TraceID,
			DistanceKM: input.DistanceKM, ObservedAt: now,
		})
		_ = s.services.Correlator.Pair(incidentID)
		if pair.Complete || input.StationID != "" {
			s.services.Classifier.PeerEvidence(current, now)
		}
		snapshot = current.Snapshot()
	case "expire":
		snapshot = s.services.Classifier.Expire(current, now.Add(11*time.Second))
	case "escalate":
		severity, ok := incident.ParseSeverity(input.Severity)
		if !ok {
			writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "unsupported severity"})
			return
		}
		snapshot = s.services.Desk.Escalate(current, severity, now)
	case "claim":
		if input.Operator == "" {
			writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "operator is required"})
			return
		}
		snapshot = s.services.Desk.Claim(current, input.Operator, now)
	case "reassign":
		if input.Operator == "" {
			writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "operator is required"})
			return
		}
		snapshot = s.services.Desk.Reassign(current, input.Operator, now)
	case "add-evidence":
		snapshot = s.services.Desk.AddEvidence(current, input.Kind, input.Detail, input.Operator, now)
	case "recover":
		snapshot = s.services.Desk.Recover(current, input.Operator, input.Detail, now)
	default:
		writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "unsupported incident action"})
		return
	}
	if err := s.services.Persist("incident", incidentID, snapshot); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}
