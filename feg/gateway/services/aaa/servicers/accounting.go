/*
Copyright 2020 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// package servcers implements WiFi AAA GRPC services
package servicers

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/feg/cloud/go/protos/mconfig"
	"magma/feg/gateway/registry"
	"magma/feg/gateway/services/aaa"
	"magma/feg/gateway/services/aaa/events"
	"magma/feg/gateway/services/aaa/metrics"
	"magma/feg/gateway/services/aaa/pipelined"
	"magma/feg/gateway/services/aaa/protos"
	"magma/feg/gateway/services/aaa/session_manager"
	"magma/gateway/directoryd"
	lte_protos "magma/lte/cloud/go/protos"
	orcprotos "magma/orc8r/lib/go/protos"
)

type accountingService struct {
	sessions    aaa.SessionTable
	config      *mconfig.AAAConfig
	sessionTout time.Duration // Idle Session Timeout
}

const (
	imsiPrefix = "IMSI"
)

// NewEapAuthenticator returns a new instance of EAP Auth service
func NewAccountingService(sessions aaa.SessionTable, cfg *mconfig.AAAConfig) (*accountingService, error) {
	return &accountingService{
		sessions:    sessions,
		config:      cfg,
		sessionTout: GetIdleSessionTimeout(cfg),
	}, nil
}

// Start implements Radius Acct-Status-Type: Start endpoint
func (srv *accountingService) Start(ctx context.Context, aaaCtx *protos.Context) (*protos.AcctResp, error) {
	if aaaCtx == nil {
		return &protos.AcctResp{}, status.Errorf(codes.InvalidArgument, "Nil AAA Context")
	}
	sid := aaaCtx.GetSessionId()
	s := srv.sessions.GetSession(sid)
	if s == nil {
		return &protos.AcctResp{}, Errorf(
			codes.FailedPrecondition, "Accounting Start: Session %s was not authenticated", sid)
	}
	var (
		err    error
		csResp *protos.CreateSessionResp
	)
	if srv.config.GetAccountingEnabled() && !srv.config.GetCreateSessionOnAuth() {
		csResp, err = srv.CreateSession(ctx, aaaCtx)
		if err == nil {
			s.Lock()
			s.GetCtx().AcctSessionId = csResp.GetSessionId()
			s.Unlock()
		}
	} else {
		srv.sessions.SetTimeout(sid, srv.sessionTout, srv.timeoutSessionNotifier)
	}
	return &protos.AcctResp{}, err
}

// InterimUpdate implements Radius Acct-Status-Type: Interim-Update endpoint
func (srv *accountingService) InterimUpdate(_ context.Context, ur *protos.UpdateRequest) (*protos.AcctResp, error) {
	if ur == nil {
		return &protos.AcctResp{}, Errorf(codes.InvalidArgument, "Nil Update Request")
	}
	sid := ur.GetCtx().GetSessionId()
	s := srv.sessions.GetSession(sid)
	if s == nil {
		return &protos.AcctResp{}, Errorf(
			codes.FailedPrecondition, "Accounting Update: Session %s was not authenticated", sid)
	}
	srv.sessions.SetTimeout(sid, srv.sessionTout, srv.timeoutSessionNotifier)

	imsi := metrics.DecorateIMSI(s.GetCtx().GetImsi())
	metrics.OctetsIn.WithLabelValues(s.GetCtx().GetApn(), imsi).Add(float64(ur.GetOctetsIn()))
	metrics.OctetsOut.WithLabelValues(s.GetCtx().GetApn(), imsi).Add(float64(ur.GetOctetsOut()))

	return &protos.AcctResp{}, nil
}

// Stop implements Radius Acct-Status-Type: Stop endpoint
func (srv *accountingService) Stop(_ context.Context, req *protos.StopRequest) (*protos.AcctResp, error) {
	if req == nil {
		return &protos.AcctResp{}, status.Errorf(codes.InvalidArgument, "Nil Stop Request")
	}
	sid := req.GetCtx().GetSessionId()
	s := srv.sessions.RemoveSession(sid)
	if s == nil {
		// Log error and return OK, no need to stop accounting for already removed session
		glog.Warningf("Accounting Stop: Session %s is not found", sid)
		return &protos.AcctResp{}, nil
	}

	s.Lock()
	sessionImsi := s.GetCtx().GetImsi()
	apn := s.GetCtx().GetApn()
	s.Unlock()

	imsi := metrics.DecorateIMSI(sessionImsi)
	var err error
	if srv.config.GetAccountingEnabled() {
		req := &lte_protos.LocalEndSessionRequest{
			Sid: makeSID(imsi),
			Apn: apn,
		}
		_, err = session_manager.EndSession(req)
		if err != nil {
			err = Error(codes.Unavailable, err)
		}
		metrics.EndSession.WithLabelValues(apn, imsi).Inc()
	} else {
		deleteRequest := &orcprotos.DeleteRecordRequest{
			Id: sessionImsi,
		}
		directoryd.DeleteRecord(deleteRequest)
	}
	metrics.AcctStop.WithLabelValues(apn, imsi)

	if err != nil && srv.config.GetEventLoggingEnabled() {
		events.LogSessionTerminationFailedEvent(req.GetCtx(), events.AccountingStop, err.Error())
	} else if srv.config.GetEventLoggingEnabled() {
		events.LogSessionTerminationSucceededEvent(req.GetCtx(), events.AccountingStop)
	}
	return &protos.AcctResp{}, err
}

// CreateSession is an "outbound" RPC for session manager which can be called from start()
func (srv *accountingService) CreateSession(
	_ context.Context, aaaCtx *protos.Context) (CSR *protos.CreateSessionResp, err error) {

	startime := time.Now()

	mac, err := net.ParseMAC(aaaCtx.GetMacAddr())
	if err != nil {
		return &protos.CreateSessionResp{}, Errorf(codes.InvalidArgument, "Invalid MAC Address: %v", err)
	}
	subscriberId := makeSID(aaaCtx.GetImsi())
	err = installMacFlowOrRecycleSession(subscriberId, aaaCtx, srv.sessions)
	if err != nil {
		return nil, Errorf(codes.Internal, "Error on install mac flow: %v", err)
	}

	csResp, err := createSessionOnSessionManager(mac, subscriberId, aaaCtx)
	if err == nil {
		metrics.CreateSessionLatency.Observe(time.Since(startime).Seconds())
	} else {
		// TODO: do we really need to remove the flow?
		pipelined.DeleteUeMacFlow(subscriberId, aaaCtx)
	}

	return &protos.CreateSessionResp{SessionId: csResp.GetSessionId()}, err
}

// TerminateSession is an "inbound" RPC from session manager to notify accounting of a client session termination
func (srv *accountingService) TerminateSession(
	ctx context.Context, req *protos.TerminateSessionRequest) (*protos.AcctResp, error) {

	sid := req.GetRadiusSessionId()
	s := srv.sessions.RemoveSession(sid)

	if s == nil {
		return &protos.AcctResp{}, Errorf(codes.FailedPrecondition, "Session %s is not found", sid)
	}

	s.Lock()
	sctx := s.GetCtx()
	imsi := sctx.GetImsi()
	apn := sctx.GetApn()
	s.Unlock()

	metrics.SessionTerminate.WithLabelValues(apn, metrics.DecorateIMSI(imsi)).Inc()

	if !strings.HasPrefix(imsi, imsiPrefix) {
		imsi = imsiPrefix + imsi
	}
	if imsi != req.GetImsi() {
		return &protos.AcctResp{}, Errorf(
			codes.InvalidArgument, "Mismatched IMSI: %s != %s of session %s", req.GetImsi(), imsi, sid)
	}

	conn, err := registry.GetConnection(registry.RADIUS)
	if err != nil {
		return &protos.AcctResp{}, Errorf(codes.Unavailable, "Error getting Radius RPC Connection: %v", err)
	}
	radcli := protos.NewAuthorizationClient(conn)
	_, err = radcli.Disconnect(ctx, &protos.DisconnectRequest{Ctx: sctx})
	if err != nil {
		err = Error(codes.Internal, err)
	}
	return &protos.AcctResp{}, err
}

// EndTimedOutSession is an "inbound" -> session manager AND "outbound" -> Radius server notification of a timed out
// session. It should be called for a timed out and recently removed from the sessions table session.
func (srv *accountingService) EndTimedOutSession(aaaCtx *protos.Context) error {
	if aaaCtx == nil {
		errMsg := fmt.Sprintf("Nil AAA Context")
		if srv.config.GetEventLoggingEnabled() {
			events.LogSessionTerminationFailedEvent(aaaCtx, "Session Timeout Notification", errMsg)
		}
		return status.Errorf(codes.InvalidArgument, errMsg)
	}
	var err, radErr error

	if srv.config.GetAccountingEnabled() {
		req := &lte_protos.LocalEndSessionRequest{
			Sid: makeSID(aaaCtx.GetImsi()),
			Apn: aaaCtx.GetApn(),
		}
		_, err = session_manager.EndSession(req)
		metrics.EndSession.WithLabelValues(aaaCtx.GetApn(), metrics.DecorateIMSI(aaaCtx.GetImsi())).Inc()
	} else {
		deleteRequest := &orcprotos.DeleteRecordRequest{
			Id: aaaCtx.GetImsi(),
		}
		directoryd.DeleteRecord(deleteRequest)
	}

	conn, radErr := registry.GetConnection(registry.RADIUS)
	if radErr != nil {
		radErr = status.Errorf(codes.Unavailable, "Session Timeout Notification Radius Connection Error: %v", radErr)
	} else {
		_, radErr = protos.NewAuthorizationClient(conn).Disconnect(
			context.Background(), &protos.DisconnectRequest{Ctx: aaaCtx})
	}
	if radErr != nil {
		if err != nil {
			err = Errorf(
				codes.Internal, "Session Timeout Notification errors; session manager: %v, Radius: %v", err, radErr)
		} else {
			err = Error(codes.Unavailable, radErr)
		}
	}
	if err != nil && srv.config.GetEventLoggingEnabled() {
		events.LogSessionTerminationFailedEvent(aaaCtx, events.SessionTimeout, err.Error())
	} else if srv.config.GetEventLoggingEnabled() {
		events.LogSessionTerminationSucceededEvent(aaaCtx, events.SessionTimeout)
	}
	return err
}

// AddSessions is an "inbound" RPC from session manager to bulk add existing sessions
func (srv *accountingService) AddSessions(ctx context.Context, sessions *protos.AddSessionsRequest) (*protos.AcctResp, error) {
	failed := []string{}
	for _, session := range sessions.GetSessions() {
		if strings.HasPrefix(session.GetImsi(), imsiPrefix) {
			session.Imsi = strings.TrimPrefix(session.GetImsi(), imsiPrefix)
		}
		_, err := srv.sessions.AddSession(session, srv.sessionTout, srv.timeoutSessionNotifier, true)
		if err != nil {
			failed = append(failed, session.GetImsi())
		}
	}
	if len(failed) > 0 {
		return &protos.AcctResp{}, fmt.Errorf("Unable to add the session for the following IMSIs: %v", failed)
	}
	return &protos.AcctResp{}, nil
}

// installMacFlowOrIPFIXflow installs a new mac flow if it is a brand new session or
// reinstalls IPFIX flows in case the CORE session already existed (recycle session)
// Note that the existence of a core session will trigger a recylce process on sessiond too
func installMacFlowOrRecycleSession(sid *lte_protos.SubscriberID, aaaCtx *protos.Context, sessions aaa.SessionTable) error {
	if isSessionAlreadyStored(aaaCtx.GetImsi(), sessions) == false {
		// (new session) install MAC flows for new session
		glog.V(2).Infof("Install new  mac flows for %s", aaaCtx.GetImsi())
		return pipelined.AddUeMacFlow(sid, aaaCtx)
	} else {
		// (recycle session) reinstall IPFIX flows for an existing session
		// the session will be modified by store in memory it self
		glog.V(2).Infof("Update IPFix flows (recycle Session) for %s", aaaCtx.GetImsi())
		return pipelined.UpdateIPFIXFlow(sid, aaaCtx)
	}
}

func isSessionAlreadyStored(imsi string, sessions aaa.SessionTable) bool {
	oldSession := sessions.GetSessionByImsi(imsi)
	if oldSession != nil {
		oldSession.Lock()
		defer oldSession.Unlock()
		return len(oldSession.GetCtx().GetAcctSessionId()) > 0
	}
	return false
}

func createSessionOnSessionManager(mac net.HardwareAddr, subscriberId *lte_protos.SubscriberID,
	aaaCtx *protos.Context) (*lte_protos.LocalCreateSessionResponse, error) {
	req := &lte_protos.LocalCreateSessionRequest{
		// TODO deprecate the fields below
		Sid:             subscriberId,
		UeIpv4:          aaaCtx.GetIpAddr(),
		Apn:             aaaCtx.GetApn(),
		Msisdn:          ([]byte)(aaaCtx.GetMsisdn()),
		RatType:         lte_protos.RATType_TGPP_WLAN,
		HardwareAddr:    mac,
		RadiusSessionId: aaaCtx.GetSessionId(),
		// TODO the fields above will be replaced by CommonContext and
		// RatSpecificContext below.
		CommonContext: &lte_protos.CommonSessionContext{
			Sid:     subscriberId,
			UeIpv4:  aaaCtx.GetIpAddr(),
			Apn:     aaaCtx.GetApn(),
			Msisdn:  ([]byte)(aaaCtx.GetMsisdn()),
			RatType: lte_protos.RATType_TGPP_WLAN,
		},
		RatSpecificContext: &lte_protos.RatSpecificContext{
			Context: &lte_protos.RatSpecificContext_WlanContext{
				WlanContext: &lte_protos.WLANSessionContext{
					MacAddrBinary:   mac,
					MacAddr:         aaaCtx.GetMacAddr(),
					RadiusSessionId: aaaCtx.GetSessionId(),
				},
			},
		},
	}
	return session_manager.CreateSession(req)
}

func (srv *accountingService) timeoutSessionNotifier(s aaa.Session) error {
	if srv != nil && s != nil {
		return srv.EndTimedOutSession(s.GetCtx())
	}
	return nil
}

func makeSID(imsi string) *lte_protos.SubscriberID {
	if !strings.HasPrefix(imsi, imsiPrefix) {
		imsi = imsiPrefix + imsi
	}
	return &lte_protos.SubscriberID{Id: imsi, Type: lte_protos.SubscriberID_IMSI}
}