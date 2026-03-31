FROM python:3.12-slim AS builder

WORKDIR /app
COPY filter-service/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
RUN python -m spacy download en_core_web_lg

FROM python:3.12-slim

WORKDIR /app
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin
COPY filter-service/ .
COPY proto/ /app/proto/

# Generate protobuf stubs (flat output — main.py imports filter_pb2 directly)
RUN python -m grpc_tools.protoc \
    -I/app/proto/filter/v1 \
    --python_out=/app \
    --grpc_python_out=/app \
    filter.proto

EXPOSE 50051
CMD ["python", "main.py"]
