"""gRPC FilterService implementation."""

import logging

import grpc
import filter_pb2
import filter_pb2_grpc
from pii_scanner import PIIScanner

logger = logging.getLogger(__name__)


class FilterService(filter_pb2_grpc.FilterServiceServicer):
    """Implements the aegis.filter.v1.FilterService gRPC interface."""

    def __init__(self):
        self.scanner = PIIScanner()
        logger.info("PII scanner initialized")

    def ScanPII(self, request, context):
        """Scan text for PII."""
        try:
            result = self.scanner.scan(
                text=request.text,
                classification=request.classification,
            )

            detections = [
                filter_pb2.PIIDetection(
                    entity_type=d.entity_type,
                    start=d.start,
                    end=d.end,
                    score=d.score,
                )
                for d in result.detections
            ]

            return filter_pb2.ScanPIIResponse(
                detected=result.detected,
                detections=detections,
                redacted_text=result.redacted_text,
            )
        except Exception as e:
            logger.error("PII scan error: %s", e)
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(e))
            return filter_pb2.ScanPIIResponse()
