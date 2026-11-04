# The reference implementation for the interoperability matrix: pymodbus, the
# Python Modbus stack, with one script for each direction.
#
#   docker build -t gmodbus-interop-pymodbus:3.8.6 interop/docker/pymodbus
#
# The tag is pinned to the pymodbus version on purpose: the client and datastore
# APIs move between releases, and a floating tag would turn a green matrix red
# without a code change here. The Go test builds this image when it is missing.
FROM python:3.13-slim

RUN pip install --no-cache-dir pymodbus==3.8.6

WORKDIR /interop
COPY slave.py master.py ./

# The default is the slave; direction B overrides the entrypoint with master.py.
ENTRYPOINT ["python", "slave.py"]
