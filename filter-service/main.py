"""gRPC server entry point for the AEGIS PII filter service."""

import logging
import os
import sys
from concurrent import futures

import grpc
import filter_pb2_grpc
from service import FilterService

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
logger = logging.getLogger("aegis-filter-nlp")


def serve():
    port = os.environ.get("GRPC_PORT", "50051")
    workers = int(os.environ.get("GRPC_WORKERS", "4"))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=workers))
    filter_pb2_grpc.add_FilterServiceServicer_to_server(FilterService(), server)
    server.add_insecure_port(f"[::]:{port}")

    logger.info("starting PII filter service on port %s with %d workers", port, workers)
    server.start()

    try:
        server.wait_for_termination()
    except KeyboardInterrupt:
        logger.info("shutting down")
        server.stop(grace=5)


if __name__ == "__main__":
    serve()
