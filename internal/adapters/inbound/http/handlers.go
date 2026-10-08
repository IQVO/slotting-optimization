package http

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// planIDParam returns the {planId} path segment, unescaped (an encoded '/' or
// space reaches the use case and is rejected there as invalid-plan-id).
func planIDParam(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "planId")
	id, err := url.PathUnescape(raw)
	if err != nil {
		return "", slotplan.ErrInvalidPlanID
	}
	return id, nil
}

// intParam reads an optional positive integer query parameter. Absent is
// (0, nil); present but not a positive integer is invalid (the use case
// owns the upper bound).
func intParam(r *http.Request, name string, invalid error) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: %s must be a positive integer", invalid, name)
	}
	return n, nil
}

// handleGenerateSlotPlan backs POST /slot-plans: 201 with the Draft plan and
// its Location.
func (s *Server) handleGenerateSlotPlan(w http.ResponseWriter, r *http.Request) {
	var req generateSlotPlanRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := usecases.GenerateInput{}
	if req.SiteID != nil {
		if *req.SiteID == "" {
			writeError(w, r, slotplan.ErrInvalidSiteID) // explicit "" is out of range, not "absent"
			return
		}
		in.SiteID = *req.SiteID
	}
	if req.LookbackDays != nil {
		if *req.LookbackDays == 0 {
			writeError(w, r, usecases.ErrInvalidLookback)
			return
		}
		in.LookbackDays = *req.LookbackDays
	}
	p, err := s.GeneratePlan.Handle(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/slot-plans/"+url.PathEscape(string(p.ID())))
	writeJSON(w, http.StatusCreated, toSlotPlan(p))
}

// handleListSlotPlans backs GET /slot-plans.
func (s *Server) handleListSlotPlans(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", usecases.ErrInvalidLimit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := s.ListPlans.Handle(r.Context(), usecases.ListPlansQuery{
		Limit: limit, Cursor: q.Get("cursor"), SiteID: q.Get("siteId"), State: q.Get("state"),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSlotPlanPage(page))
}

// handleGetSlotPlan backs GET /slot-plans/{planId}.
func (s *Server) handleGetSlotPlan(w http.ResponseWriter, r *http.Request) {
	id, err := planIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.GetPlan.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSlotPlan(p))
}

// handleApproveSlotPlan backs POST /slot-plans/{planId}/approve.
func (s *Server) handleApproveSlotPlan(w http.ResponseWriter, r *http.Request) {
	id, err := planIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.ApprovePlan.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSlotPlan(p))
}

// handleRejectSlotPlan backs POST /slot-plans/{planId}/reject.
func (s *Server) handleRejectSlotPlan(w http.ResponseWriter, r *http.Request) {
	id, err := planIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req rejectSlotPlanRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.RejectPlan.Handle(r.Context(), id, req.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSlotPlan(p))
}

// handleListForwardSlots backs GET /forward-slots.
func (s *Server) handleListForwardSlots(w http.ResponseWriter, r *http.Request) {
	m, err := s.ListForwardSlots.Handle(r.Context(), r.URL.Query().Get("siteId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toForwardSlots(m))
}

// handleListSkuVelocity backs GET /sku-velocity.
func (s *Server) handleListSkuVelocity(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", usecases.ErrInvalidLimit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	window, err := intParam(r, "windowDays", usecases.ErrInvalidLimit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.ListSkuVelocity.Handle(r.Context(), usecases.SkuVelocityQuery{
		SiteID: r.URL.Query().Get("siteId"), Limit: limit, WindowDays: window,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSkuVelocity(res))
}
