package kafka_test

import (
	"strconv"
	"strings"
)

// The consumed messages are the producers' own contracts as published in
// apis/asyncapi.yaml, restated here as raw JSON (never built with this
// service's helper, which only produces this service's own types).

func ce(id, source, typ, subject, dataschema, data string) []byte {
	return []byte(`{"specversion":"1.0","id":"` + id + `","source":"` + source + `","type":"` + typ + `","subject":"` + subject + `",` +
		`"time":"2026-10-07T09:00:00Z","datacontenttype":"application/json","dataschema":"` + dataschema + `","data":` + data + `}`)
}

func demandMsg(id, order string, line int, site, sku string, units int, state string) []byte {
	subject := order + "/line/" + strconv.Itoa(line)
	data := `{"source_order_id":"` + order + `","line_no":` + strconv.Itoa(line) + `,"site_id":"` + site + `","sku":"` + sku + `","demanded_units":` + strconv.Itoa(units) +
		`,"due_at":"2026-10-09T10:00:00Z","state":"` + state + `","assignment_version":"static-site-v1"}`
	return ce(id, "/warehouse/order-management", "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged", subject,
		"urn:warehouse:order-management:events:SiteSkuDemandChanged:v1", data)
}

func classifiedMsg(id, sku, data string) []byte {
	return ce(id, "/warehouse/product-master", "com.warehouse.wms.product-master.product.ProductClassified", sku,
		"urn:warehouse:product-master:events:ProductClassified:v1", data)
}

func physicalMsg(id, typ, sku, data string) []byte {
	name := typ[strings.LastIndex(typ, ".")+1:]
	return ce(id, "/warehouse/product-master", typ, sku, "urn:warehouse:product-master:events:"+name+":v1", data)
}

func zoneMsg(id, zoneID, data string) []byte {
	return ce(id, "/warehouse/facility-layout", "com.warehouse.wms.facility-layout.zone.ZoneRegistered", zoneID,
		"urn:warehouse:facility-layout:events:ZoneRegistered:v1", data)
}

func slotMsg(id, typ, code, data string) []byte {
	name := typ[strings.LastIndex(typ, ".")+1:]
	return ce(id, "/warehouse/facility-layout", typ, code, "urn:warehouse:facility-layout:events:"+name+":v1", data)
}

// legacyFlat is the retired flat envelope; a consumer must reject it.
const legacyFlat = `{"event_id":"e-1","event_type":"SiteSkuDemandChanged","occurred_at":"2026-10-07T09:00:00Z","source_order_id":"o1","sku":"SKU-1"}`
