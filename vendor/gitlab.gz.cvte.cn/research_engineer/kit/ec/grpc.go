package ec

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCStatus 实现grpc的status
func (ec ErrorCode) GRPCStatus() *status.Status {
	return status.New(codes.Code(ec.Status), ec.Msg)
}

// FromGRPCError 从grpc err中恢复成ErrorCode
func FromGRPCError(err error) *ErrorCode {
	st, ok := status.FromError(err)
	if !ok {
		return New(err.Error())
	}
	return NewErrorCode(int32(st.Code()), st.Message())
}
