"""A pymodbus slave with a fixed datastore, for the interoperability matrix.

The Go test starts this script inside the reference container and asserts on the
values below; keeping them here means the assertion and the slave are one file.
pymodbus 3.8 maps Modbus address N to datastore index N+1 (its datastore is
one-based), so every block carries a leading pad and address 0 is values[0].
"""

import sys

from pymodbus.datastore import ModbusSequentialDataBlock, ModbusServerContext, ModbusSlaveContext
from pymodbus.server import StartTcpServer


def block(values, size=100):
    """A datastore block whose Modbus address 0 holds values[0]."""
    values = list(values)
    return ModbusSequentialDataBlock(0, [0] + values + [0] * (size - len(values)))


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 5020
    store = ModbusSlaveContext(
        di=block([True, False, True]),
        co=block([True, False, True, False]),
        hr=block([10, 20, 30, 40]),
        ir=block([100, 200, 300, 400]),
    )
    print("pymodbus slave listening on 0.0.0.0:%d" % port, flush=True)
    StartTcpServer(context=ModbusServerContext(slaves=store, single=True), address=("0.0.0.0", port))


if __name__ == "__main__":
    main()
