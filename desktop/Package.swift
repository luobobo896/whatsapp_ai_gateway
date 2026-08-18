// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "WDAFarmGateway",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "WDAFarmGateway", path: "Sources/WDAFarmGateway")
    ]
)
