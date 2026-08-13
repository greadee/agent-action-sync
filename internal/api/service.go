package api

import (
	"context"
	"errors"
	"fmt"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
)

type AdministrationService interface {
	AdministrationReadiness
	ListShares(ctx context.Context, page storage.PageRequest) (storage.Page[storage.AdminShare], error)
	ListDevices(ctx context.Context, page storage.PageRequest) (storage.Page[storage.AdminDevice], error)
	GetDevice(ctx context.Context, id core.DeviceID) (storage.AdminDevice, error)
	ListPermissions(ctx context.Context, query storage.PermissionQuery) (storage.Page[storage.AdminPermission], error)
	ListJobs(ctx context.Context, query storage.JobQuery) (storage.Page[storage.AdminJob], error)
	GetJob(ctx context.Context, id string) (storage.AdminJob, error)
	ListAuditEvents(ctx context.Context, query storage.AuditEventQuery) (storage.Page[storage.AdminAuditEvent], error)
}

type AdministrationServiceOptions struct {
	Queries     storage.AdministrationQueryStore
	Ready       func() bool
	Runtime     func() RuntimeSnapshot
	Diagnostics func() syncengine.DiagnosticReport
	Scan        func(context.Context, core.ShareID) error
	Control     func(context.Context, string, JobActionName) (storage.AdminJob, error)
	Pairing     *PairingCoordinator
}

type LocalAdministrationService struct {
	queries     storage.AdministrationQueryStore
	ready       func() bool
	runtime     func() RuntimeSnapshot
	diagnostics func() syncengine.DiagnosticReport
	scan        func(context.Context, core.ShareID) error
	control     func(context.Context, string, JobActionName) (storage.AdminJob, error)
	pairing     *PairingCoordinator
}

func NewAdministrationService(options AdministrationServiceOptions) (*LocalAdministrationService, error) {
	if options.Queries == nil {
		return nil, errors.New("administration query store is required")
	}
	if options.Ready == nil {
		return nil, errors.New("administration readiness function is required")
	}
	return &LocalAdministrationService{queries: options.Queries, ready: options.Ready, runtime: options.Runtime, diagnostics: options.Diagnostics, scan: options.Scan, control: options.Control, pairing: options.Pairing}, nil
}

func (service *LocalAdministrationService) CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error) {
	if err := validateServiceContext(ctx); err != nil {
		return InvitationDTO{}, err
	}
	if service.pairing == nil {
		return InvitationDTO{}, errUnavailable
	}
	return service.pairing.CreatePairingInvitation(ctx, request)
}

func (service *LocalAdministrationService) InspectPairingInvitation(ctx context.Context, request EncodedInvitation) (InvitationInspection, error) {
	if err := validateServiceContext(ctx); err != nil {
		return InvitationInspection{}, err
	}
	if service.pairing == nil {
		return InvitationInspection{}, errUnavailable
	}
	return service.pairing.InspectPairingInvitation(ctx, request)
}

func (service *LocalAdministrationService) AcceptPairingInvitation(ctx context.Context, request AcceptanceRequest) (Acceptance, error) {
	if err := validateServiceContext(ctx); err != nil {
		return Acceptance{}, err
	}
	if service.pairing == nil {
		return Acceptance{}, errUnavailable
	}
	return service.pairing.AcceptPairingInvitation(ctx, request)
}

func (service *LocalAdministrationService) RevokePairingDevice(ctx context.Context, deviceID core.DeviceID) (Revocation, error) {
	if err := validateServiceContext(ctx); err != nil {
		return Revocation{}, err
	}
	if service.pairing == nil {
		return Revocation{}, errUnavailable
	}
	return service.pairing.RevokePairingDevice(ctx, deviceID)
}

func (service *LocalAdministrationService) Ready() bool {
	return service != nil && service.ready != nil && service.ready()
}

func (service *LocalAdministrationService) ListShares(ctx context.Context, page storage.PageRequest) (storage.Page[storage.AdminShare], error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.Page[storage.AdminShare]{}, err
	}
	result, err := service.queries.ListAdminShares(ctx, page)
	if err != nil {
		return storage.Page[storage.AdminShare]{}, fmt.Errorf("list administration shares: %w", err)
	}
	return clonePage(result, func(item storage.AdminShare) storage.AdminShare { return item }), nil
}

func (service *LocalAdministrationService) ListDevices(ctx context.Context, page storage.PageRequest) (storage.Page[storage.AdminDevice], error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.Page[storage.AdminDevice]{}, err
	}
	result, err := service.queries.ListAdminDevices(ctx, page)
	if err != nil {
		return storage.Page[storage.AdminDevice]{}, fmt.Errorf("list administration devices: %w", err)
	}
	return clonePage(result, storage.CloneAdminDevice), nil
}

func (service *LocalAdministrationService) GetDevice(ctx context.Context, id core.DeviceID) (storage.AdminDevice, error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.AdminDevice{}, err
	}
	result, err := service.queries.GetAdminDevice(ctx, id)
	if err != nil {
		return storage.AdminDevice{}, fmt.Errorf("get administration device: %w", err)
	}
	return storage.CloneAdminDevice(result), nil
}

func (service *LocalAdministrationService) ListPermissions(ctx context.Context, query storage.PermissionQuery) (storage.Page[storage.AdminPermission], error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.Page[storage.AdminPermission]{}, err
	}
	result, err := service.queries.ListAdminPermissions(ctx, query)
	if err != nil {
		return storage.Page[storage.AdminPermission]{}, fmt.Errorf("list administration permissions: %w", err)
	}
	return clonePage(result, storage.CloneAdminPermission), nil
}

func (service *LocalAdministrationService) ListJobs(ctx context.Context, query storage.JobQuery) (storage.Page[storage.AdminJob], error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.Page[storage.AdminJob]{}, err
	}
	result, err := service.queries.ListAdminJobs(ctx, query)
	if err != nil {
		return storage.Page[storage.AdminJob]{}, fmt.Errorf("list administration jobs: %w", err)
	}
	return clonePage(result, func(item storage.AdminJob) storage.AdminJob { return item }), nil
}

func (service *LocalAdministrationService) GetJob(ctx context.Context, id string) (storage.AdminJob, error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.AdminJob{}, err
	}
	result, err := service.queries.GetAdminJob(ctx, id)
	if err != nil {
		return storage.AdminJob{}, fmt.Errorf("get administration job: %w", err)
	}
	return result, nil
}

func (service *LocalAdministrationService) ListAuditEvents(ctx context.Context, query storage.AuditEventQuery) (storage.Page[storage.AdminAuditEvent], error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, err
	}
	result, err := service.queries.ListAdminAuditEvents(ctx, query)
	if err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, fmt.Errorf("list administration audit events: %w", err)
	}
	return clonePage(result, func(item storage.AdminAuditEvent) storage.AdminAuditEvent { return item }), nil
}

func validateServiceContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("administration request context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func clonePage[T any](source storage.Page[T], clone func(T) T) storage.Page[T] {
	result := storage.Page[T]{Items: make([]T, len(source.Items))}
	for index, item := range source.Items {
		result.Items[index] = clone(item)
	}
	if source.NextCursor != nil {
		cursor := *source.NextCursor
		result.NextCursor = &cursor
	}
	return result
}

var _ AdministrationService = (*LocalAdministrationService)(nil)
