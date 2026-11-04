"""A pymodbus master that reads and writes a gmodbus server, for direction B.

It runs inside the reference container, reaches the Go server on
host.docker.internal, and prints one JSON object with everything it saw. The Go
test parses that object and asserts on it; a failed exchange exits non-zero with
the reason on stderr, so the test reports the reference client's own words.

Usage: master.py <host> <port>
"""

import json
import logging
import sys

from pymodbus.client import ModbusTcpClient


def main():
    # pymodbus logs an exception response at warning level; stdout carries the
    # JSON and nothing else, so the log goes to stderr where the test can show it.
    logging.basicConfig(stream=sys.stderr, level=logging.WARNING)
    if len(sys.argv) < 3:
        sys.exit("usage: master.py <host> <port>")
    host, port = sys.argv[1], int(sys.argv[2])
    results = {}

    with ModbusTcpClient(host, port=port, timeout=5) as client:
        def check(response, what):
            if response.isError():
                sys.exit("pymodbus %s failed: %s" % (what, response))
            return response

        results["coils"] = check(client.read_coils(0, count=3, slave=1), "read_coils").bits[:3]
        results["discrete_inputs"] = check(
            client.read_discrete_inputs(0, count=3, slave=1), "read_discrete_inputs"
        ).bits[:3]
        results["holding"] = check(
            client.read_holding_registers(0, count=4, slave=1), "read_holding_registers"
        ).registers
        results["input"] = check(
            client.read_input_registers(0, count=4, slave=1), "read_input_registers"
        ).registers

        check(client.write_register(2, 0x1234, slave=1), "write_register")
        results["written_register"] = check(
            client.read_holding_registers(2, count=1, slave=1), "read back the written register"
        ).registers

        check(client.write_coils(4, [True, False, True], slave=1), "write_coils")
        results["written_coils"] = check(
            client.read_coils(4, count=3, slave=1), "read back the written coils"
        ).bits[:3]

        check(client.write_registers(8, [7, 8], slave=1), "write_registers")
        results["written_registers"] = check(
            client.read_holding_registers(8, count=2, slave=1), "read back the written registers"
        ).registers

        # An address the server does not hold must come back as an exception, not
        # as a timeout or a silent zero.
        rejected = client.read_holding_registers(2000, count=2, slave=1)
        results["exception"] = rejected.isError()
        results["exception_code"] = getattr(rejected, "exception_code", None)

    print(json.dumps(results), flush=True)


if __name__ == "__main__":
    main()
