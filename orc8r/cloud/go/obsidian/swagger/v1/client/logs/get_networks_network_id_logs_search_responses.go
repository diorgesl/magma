// Code generated by go-swagger; DO NOT EDIT.

package logs

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"

	strfmt "github.com/go-openapi/strfmt"

	models "magma/orc8r/cloud/go/obsidian/swagger/v1/models"
)

// GetNetworksNetworkIDLogsSearchReader is a Reader for the GetNetworksNetworkIDLogsSearch structure.
type GetNetworksNetworkIDLogsSearchReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *GetNetworksNetworkIDLogsSearchReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 200:
		result := NewGetNetworksNetworkIDLogsSearchOK()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	default:
		result := NewGetNetworksNetworkIDLogsSearchDefault(response.Code())
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		if response.Code()/100 == 2 {
			return result, nil
		}
		return nil, result
	}
}

// NewGetNetworksNetworkIDLogsSearchOK creates a GetNetworksNetworkIDLogsSearchOK with default headers values
func NewGetNetworksNetworkIDLogsSearchOK() *GetNetworksNetworkIDLogsSearchOK {
	return &GetNetworksNetworkIDLogsSearchOK{}
}

/*GetNetworksNetworkIDLogsSearchOK handles this case with default header values.

Success
*/
type GetNetworksNetworkIDLogsSearchOK struct {
	Payload []*models.ElasticHit
}

func (o *GetNetworksNetworkIDLogsSearchOK) Error() string {
	return fmt.Sprintf("[GET /networks/{network_id}/logs/search][%d] getNetworksNetworkIdLogsSearchOK  %+v", 200, o.Payload)
}

func (o *GetNetworksNetworkIDLogsSearchOK) GetPayload() []*models.ElasticHit {
	return o.Payload
}

func (o *GetNetworksNetworkIDLogsSearchOK) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	// response payload
	if err := consumer.Consume(response.Body(), &o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewGetNetworksNetworkIDLogsSearchDefault creates a GetNetworksNetworkIDLogsSearchDefault with default headers values
func NewGetNetworksNetworkIDLogsSearchDefault(code int) *GetNetworksNetworkIDLogsSearchDefault {
	return &GetNetworksNetworkIDLogsSearchDefault{
		_statusCode: code,
	}
}

/*GetNetworksNetworkIDLogsSearchDefault handles this case with default header values.

Unexpected Error
*/
type GetNetworksNetworkIDLogsSearchDefault struct {
	_statusCode int

	Payload *models.Error
}

// Code gets the status code for the get networks network ID logs search default response
func (o *GetNetworksNetworkIDLogsSearchDefault) Code() int {
	return o._statusCode
}

func (o *GetNetworksNetworkIDLogsSearchDefault) Error() string {
	return fmt.Sprintf("[GET /networks/{network_id}/logs/search][%d] GetNetworksNetworkIDLogsSearch default  %+v", o._statusCode, o.Payload)
}

func (o *GetNetworksNetworkIDLogsSearchDefault) GetPayload() *models.Error {
	return o.Payload
}

func (o *GetNetworksNetworkIDLogsSearchDefault) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}
