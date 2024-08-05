#!/bin/bash

ip link add dummy0 type dummy
ip link set up dev dummy0
ip addr add 169.254.169.13/32 dev dummy0

